package service

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/util/common"

	"gorm.io/gorm"
)

type NodeService struct {
	InboundService
	ServicesService
	SettingService
}

func (s *NodeService) Get(ids string) (*[]model.Node, error) {
	if ids == "" {
		return s.GetAll()
	}
	return s.getById(ids)
}

func (s *NodeService) getById(ids string) (*[]model.Node, error) {
	var nodes []model.Node
	db := database.GetDB()
	err := db.Model(model.Node{}).Where("id in ?", strings.Split(ids, ",")).Scan(&nodes).Error
	if err != nil {
		return nil, err
	}
	return &nodes, nil
}

func (s *NodeService) GetAll() (*[]model.Node, error) {
	db := database.GetDB()
	nodes := []model.Node{}
	err := db.Model(model.Node{}).Scan(&nodes).Error
	if err != nil {
		return nil, err
	}

	// Update status dynamically based on LastHeartbeat (e.g. 2 minutes)
	now := time.Now().Unix()
	for i := range nodes {
		if now-nodes[i].LastHeartbeat < 120 {
			// Online
		}
	}
	return &nodes, nil
}

func (s *NodeService) Save(tx *gorm.DB, act string, data json.RawMessage, hostname string) error {
	var err error

	switch act {
	case "new", "edit":
		var node model.Node
		err = json.Unmarshal(data, &node)
		if err != nil {
			return err
		}

		err = tx.Save(&node).Error
		if err != nil {
			return err
		}
		var inbounds []model.Inbound
		err = tx.Model(model.Inbound{}).Preload("Tls").Joins("CROSS JOIN json_each(inbounds.nodes)").
			Where("json_each.value = ?", node.Id).Find(&inbounds).Error
		if err != nil {
			return err
		}
		if len(inbounds) > 0 {
			for idx := range inbounds {
				i := &inbounds[idx]
				var nodeIds []uint
				err = json.Unmarshal(i.Nodes, &nodeIds)
				if err != nil {
					return err
				}
				var nodes []model.Node
				err = tx.Model(model.Node{}).Where("id IN ?", nodeIds).Find(&nodes).Error
				if err != nil {
					return err
				}
				var addrs []map[string]interface{}
				inbound, err := i.MarshalFull()
				if err != nil {
					return err
				}
				for _, m := range nodes {
					addrs = append(addrs, map[string]interface{}{"server": m.Addr, "server_port": (*inbound)["listen_port"]})
				}
				i.Addrs, err = json.Marshal(addrs)
				if err != nil {
					return err
				}
				err = tx.Save(i).Error
				if err != nil {
					return err
				}
			}
			err = s.UpdateLinksByInboundChange(tx, &inbounds, hostname, "")
			if err != nil {
				return err
			}
		}
	case "del":
		var id uint
		err = json.Unmarshal(data, &id)
		if err != nil {
			return err
		}
		var inboundCount int64
		err = tx.Model(model.Inbound{}).Joins("CROSS JOIN json_each(inbounds.nodes)").
			Where("json_each.value = ?", id).Count(&inboundCount).Error
		if err != nil {
			return err
		}
		if inboundCount > 0 {
			return common.NewError("node in use")
		}
		err = tx.Where("id = ?", id).Delete(model.Node{}).Error
		if err != nil {
			return err
		}
	default:
		return common.NewErrorf("unknown action: %s", act)
	}
	return nil
}

func (s *NodeService) UpdateStatus(token string, status model.NodeStatus) error {
	db := database.GetDB()
	return db.Model(model.Node{}).Where("token = ?", token).Updates(map[string]interface{}{
		"last_heartbeat": time.Now().Unix(),
		"cpu":            status.CPU,
		"mem":            status.Mem,
		"uptime":         status.Uptime,
	}).Error
}

func (s *NodeService) SaveTraffic(stats []model.Stats) error {
	if len(stats) == 0 {
		return nil
	}
	db := database.GetDB()
	tx := db.Begin()
	var err error
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	for i := range stats {
		stat := &stats[i]
		if stat.Resource == "user" {
			if stat.Direction {
				err = tx.Model(model.Client{}).Where("name = ?", stat.Tag).
					UpdateColumn("up", gorm.Expr("up + ?", stat.Traffic)).Error
			} else {
				err = tx.Model(model.Client{}).Where("name = ?", stat.Tag).
					UpdateColumn("down", gorm.Expr("down + ?", stat.Traffic)).Error
			}
			if err != nil {
				return err
			}
		}
	}

	now := time.Now().Unix()
	onlineResources.mu.Lock()
	defer onlineResources.mu.Unlock()

	for _, stat := range stats {
		if stat.Direction {
			switch stat.Resource {
			case "inbound":
				onlineResources.Inbound[stat.Tag] = now
			case "outbound":
				onlineResources.Outbound[stat.Tag] = now
			case "user":
				onlineResources.User[stat.Tag] = now
			case "node":
				onlineResources.Node[stat.Tag] = now
			}
		}
	}

	trafficAge, err := s.SettingService.GetTrafficAge()
	if err != nil || trafficAge <= 0 {
		return nil
	}

	return tx.Create(&stats).Error
}

func (s *NodeService) AppendConfigForNode(db *gorm.DB, nodeId uint) ([]json.RawMessage, error) {
	inboundService := &InboundService{}
	var inboundsJson []json.RawMessage
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Preload("Tls").Joins("CROSS JOIN json_each(inbounds.nodes)").
		Where("json_each.value = ?", nodeId).Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		inboundJson, err := inbound.MarshalJSON()
		if err != nil {
			return nil, err
		}
		inboundJson, err = inboundService.addUsers(db, inboundJson, inbound.Id, inbound.Type)
		if err != nil {
			return nil, err
		}
		inboundsJson = append(inboundsJson, inboundJson)
	}
	return inboundsJson, nil
}
