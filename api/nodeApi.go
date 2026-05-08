package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/logger"
	"github.com/alireza0/s-ui/service"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"
)

const (
	Traffic    = "traffic"
	Received   = "received"
	PullConfig = "pullConfig"
	Stats      = "stats"
)

type WsAction struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type ConnectionManager struct {
	connections map[*websocket.Conn]model.Node
	mu          sync.RWMutex
}

func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{
		connections: make(map[*websocket.Conn]model.Node),
	}
}

// Add a connection to the manager
func (cm *ConnectionManager) AddConnection(ws *websocket.Conn, node model.Node) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.connections[ws] = node
}

// Remove a connection from the manager
func (cm *ConnectionManager) RemoveConnection(ws *websocket.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.connections, ws)
}

// Get all connections
func (cm *ConnectionManager) GetAllConnections() map[*websocket.Conn]model.Node {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	// Return a copy of the connections map
	result := make(map[*websocket.Conn]model.Node)
	for k, v := range cm.connections {
		result[k] = v
	}
	return result
}

type NodeAPIHandler struct {
	nodeService       *service.NodeService
	configService     *service.ConfigService
	statsService      *service.StatsService
	connectionManager *ConnectionManager
}

func NewNodeAPIHandler(g *gin.RouterGroup, n *APIHandler) {
	a := &NodeAPIHandler{
		nodeService:       &n.NodeService,
		configService:     &n.ConfigService,
		statsService:      &n.StatsService,
		connectionManager: NewConnectionManager(),
	}
	a.configService.SetNodePullService(a.connectionManager)
	a.initRouter(g)
}

func (a *NodeAPIHandler) initRouter(g *gin.RouterGroup) {
	g.Use(a.nodeAuthMiddleware())
	g.GET("/config", a.getConfig)
	g.GET("/ws", a.getWebSocket)
}

// Middleware to authenticate nodes via Token
func (a *NodeAPIHandler) nodeAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Query("token")

		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		var node model.Node
		db := database.GetDB()
		if err := db.Where("token = ?", token).First(&node).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token or node"})
			c.Abort()
			return
		}

		// Save Node object in context
		c.Set("node_ctx", node)
		c.Next()
	}
}

func (a *NodeAPIHandler) getWebSocket(c *gin.Context) {
	// Verify authentication was successful
	nodeIntf, exists := c.Get("node_ctx")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	node := nodeIntf.(model.Node)

	// Upgrade to WebSocket connection
	websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()

		// Pass the node information to the actual handler
		a.handleWebSocket(ws, node)
	}).ServeHTTP(c.Writer, c.Request)
}

// Handle WebSocket connections
func (a *NodeAPIHandler) handleWebSocket(ws *websocket.Conn, node model.Node) {
	defer ws.Close()

	// Add this connection to the manager
	a.connectionManager.AddConnection(ws, node)
	defer a.connectionManager.RemoveConnection(ws)

	// Process WebSocket communication with the node
	for {
		// Receive data from node
		var wsHandler WsAction
		if err := websocket.JSON.Receive(ws, &wsHandler); err != nil {
			// Break loop if connection closed or error occurred
			break
		}

		if wsHandler.Type == Stats {
			var status model.NodeStatus
			dataBytes, _ := json.Marshal(wsHandler.Data)
			if err := json.Unmarshal(dataBytes, &status); err == nil {
				a.nodeService.UpdateStatus(node.Token, status)
			}
		} else if wsHandler.Type == Traffic {
			var stats []model.Stats
			dataBytes, _ := json.Marshal(wsHandler.Data)
			if err := json.Unmarshal(dataBytes, &stats); err == nil {
				for i := range stats {
					stats[i].Node = node.Name
					if stats[i].Resource == "node" && stats[i].Tag == "__SELF__" {
						stats[i].Tag = node.Name
					}
				}
				a.nodeService.SaveTraffic(stats)
			}
		}

		// Process received data (example implementation)
		// Here you would typically handle various node commands/data
		response := WsAction{
			Type: Received,
			Data: wsHandler.Data,
		}

		// Send response back to node
		if err := websocket.JSON.Send(ws, response); err != nil {
			// Break loop if unable to send response
			break
		}
	}
}

func (a *NodeAPIHandler) getConfig(c *gin.Context) {
	nodeIntf, _ := c.Get("node_ctx")
	node := nodeIntf.(model.Node)

	// Fetch full config template
	configJsonPtr, err := a.configService.GetConfig("")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get config"})
		return
	}

	var singboxConfig service.SingBoxConfig
	if err := json.Unmarshal(*configJsonPtr, &singboxConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to decode config"})
		return
	}

	// Filter Inbounds specific to this Node + default node (0)
	inboundsJson, err := a.nodeService.AppendConfigForNode(database.GetDB(), node.Id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get node inbounds"})
		return
	}

	singboxConfig.Inbounds = inboundsJson

	c.JSON(http.StatusOK, singboxConfig)
}

func (cm *ConnectionManager) BroadcastPullConfig() {
	connections := cm.GetAllConnections()

	for ws, _ := range connections {
		message := WsAction{
			Type: PullConfig,
			Data: "ok",
		}

		err := websocket.JSON.Send(ws, message)
		if err != nil {
			logger.Error("Failed to send pull config:", err)
			continue
		}
	}
}
