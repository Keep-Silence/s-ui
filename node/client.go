package node

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/alireza0/s-ui/api"
	"github.com/alireza0/s-ui/config"
	"github.com/alireza0/s-ui/core"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/logger"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/net/websocket"
)

type Client struct {
	corePtr      *core.Core
	lastConfHash string
	httpClient   *http.Client
	wsConn       *websocket.Conn
}

func NewClient() *Client {
	return &Client{
		corePtr:    core.NewCore(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		wsConn:     nil,
	}
}

func (c *Client) Start() {
	logger.Info("Starting Node Client. Connecting to panel:", config.GetPanelUrl())

	// Start polling loop
	go c.connectWebSocket()
}

func (c *Client) connectWebSocket() {
	ticker := time.NewTicker(10 * time.Second) // 尝试重连的时间间隔改为10秒，避免过于频繁
	defer ticker.Stop()

	for {
		// 只有在没有活动连接时才尝试连接
		if c.wsConn == nil {
			// Parse the panel URL to construct WebSocket URL
			panelUrl := config.GetPanelUrl()
			u, err := url.Parse(panelUrl)
			if err != nil {
				logger.Warning("Failed to parse panel URL:", err)
				<-ticker.C // 等待下次重试
				continue
			}

			// Construct WebSocket URL
			wsScheme := "ws"
			if u.Scheme == "https" {
				wsScheme = "wss"
			}
			wsUrl := url.URL{Scheme: wsScheme, Host: u.Host, Path: u.Path + "/node_api/ws", RawQuery: "token=" + config.GetNodeToken()}

			// Attempt to connect to WebSocket
			conn, err := websocket.Dial(wsUrl.String(), "", panelUrl)
			if err != nil {
				logger.Warning("Failed to connect to WebSocket:", err)
				<-ticker.C
				continue
			}

			logger.Info("Successfully connected to WebSocket")
			c.pollConfig()
			c.wsConn = conn

			// Start reporting routines over WebSocket
			go c.reportStats()
			go c.reportTraffic()

			// Listen for messages from server
			for {
				var wsHandler api.WsAction
				if err := websocket.JSON.Receive(c.wsConn, &wsHandler); err != nil {
					logger.Warning("Failed to read message from WebSocket:", err)
					c.wsConn = nil
					break
				}

				switch wsHandler.Type {
				case api.Received:
					logger.Debug("Received message:", wsHandler.Data)
				case api.PullConfig:
					go c.pollConfig()
					logger.Debug("Config pull requested")
				default:
					logger.Warning("Received message without type field:", wsHandler)
				}
			}
		}

		// 等待一段时间再检查是否需要重连
		<-ticker.C
	}
}

func (c *Client) reportStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		if c.wsConn == nil {
			return
		}

		v, _ := mem.VirtualMemory()
		cPct, _ := cpu.Percent(time.Second, false)
		h, _ := host.Info()

		var status model.NodeStatus
		if len(cPct) > 0 {
			status.CPU = cPct[0]
		}
		if v != nil {
			status.Mem = v.UsedPercent
		}
		if h != nil {
			status.Uptime = h.Uptime
		}

		message := api.WsAction{
			Type: api.Stats,
			Data: status,
		}

		err := websocket.JSON.Send(c.wsConn, message)
		if err != nil {
			logger.Warning("Failed to send stats:", err)
			return
		}
		<-ticker.C
	}
}

func (c *Client) reportTraffic() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		if c.wsConn == nil {
			return
		}

		if c.corePtr.IsRunning() {
			box := c.corePtr.GetInstance()
			if box != nil {
				st := box.StatsTracker()
				if st != nil {
					stats := st.GetStats()
					if len(*stats) > 0 {
						var nodeUp, nodeDown int64
						for _, s := range *stats {
							if s.Resource == "inbound" {
								if s.Direction {
									nodeUp += s.Traffic
								} else {
									nodeDown += s.Traffic
								}
							}
							s.Node = "__SELF__"
						}
						*stats = append(*stats, model.Stats{
							DateTime:  time.Now().Unix(),
							Resource:  "node",
							Tag:       "__SELF__",
							Node:      "__SELF__",
							Direction: true,
							Traffic:   nodeUp,
						}, model.Stats{
							DateTime:  time.Now().Unix(),
							Resource:  "node",
							Tag:       "__SELF__",
							Node:      "__SELF__",
							Direction: false,
							Traffic:   nodeDown,
						})

						message := api.WsAction{
							Type: api.Traffic,
							Data: stats,
						}
						err := websocket.JSON.Send(c.wsConn, message)
						if err != nil {
							logger.Warning("Failed to send traffic:", err)
							return
						}
					}
				}
			}
		}
		<-ticker.C
	}
}

func (c *Client) getApiUrl(path string) string {
	return config.GetPanelUrl() + "/node_api/" + path + "?token=" + config.GetNodeToken()
}

func (c *Client) pollConfig() {
	c.fetchAndApplyConfig()
}

func (c *Client) fetchAndApplyConfig() {
	resp, err := c.httpClient.Get(c.getApiUrl("config"))
	if err != nil {
		logger.Warning("Failed to fetch config from panel:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warning("Panel returned non-200 status:", resp.StatusCode)
		return
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Warning("Failed to read panel config:", err)
		return
	}

	hash := sha256.Sum256(bodyBytes)
	hashStr := hex.EncodeToString(hash[:])

	if hashStr != c.lastConfHash {
		logger.Info("New configuration received. Restarting proxy core...")
		c.lastConfHash = hashStr

		var rawConfig json.RawMessage = bodyBytes

		if c.corePtr.IsRunning() {
			c.corePtr.Stop()
		}

		err = c.corePtr.Start(rawConfig)
		if err != nil {
			logger.Error("Failed to start core with new config:", err)
		} else {
			logger.Info("Proxy core started successfully")
		}
	}
}

func (c *Client) Stop() {
	if c.corePtr.IsRunning() {
		c.corePtr.Stop()
	}

	if c.wsConn != nil {
		c.wsConn.Close()
		c.wsConn = nil
	}
}
