package model

type Node struct {
	Id            uint    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Name          string  `json:"name" form:"name"`
	Addr          string  `json:"addr" form:"addr"`
	Token         string  `json:"token" form:"token" gorm:"unique"`
	LastHeartbeat int64   `json:"last_heartbeat" form:"last_heartbeat"`
	CPU           float64 `json:"cpu" form:"cpu"`
	Mem           float64 `json:"mem" form:"mem"`
	Uptime        uint64  `json:"uptime" form:"uptime"`
}

type NodeStatus struct {
	CPU    float64 `json:"cpu"`
	Mem    float64 `json:"mem"`
	Uptime uint64  `json:"uptime"`
}
