package model

import (
	"encoding/json"
	"os"
	"strings"
)

type Inbound struct {
	Id   uint   `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Type string `json:"type" form:"type"`
	Tag  string `json:"tag" form:"tag" gorm:"unique"`

	// Foreign key to tls table
	TlsId uint `json:"tls_id" form:"tls_id"`
	Tls   *Tls `json:"tls" form:"tls" gorm:"foreignKey:TlsId;references:Id"`

	Nodes   json.RawMessage `json:"nodes" form:"nodes"`
	Addrs   json.RawMessage `json:"addrs" form:"addrs"`
	OutJson json.RawMessage `json:"out_json" form:"out_json"`
	Options json.RawMessage `json:"-" form:"-"`
}

func (i *Inbound) UnmarshalJSON(data []byte) error {
	var err error
	var raw map[string]interface{}
	if err = json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Extract fixed fields and store the rest in Options
	if val, exists := raw["id"].(float64); exists {
		i.Id = uint(val)
	}
	delete(raw, "id")
	i.Type, _ = raw["type"].(string)
	delete(raw, "type")
	i.Tag, _ = raw["tag"].(string)
	delete(raw, "tag")

	// TlsId
	if val, exists := raw["tls_id"].(float64); exists {
		i.TlsId = uint(val)
	}
	delete(raw, "tls_id")
	delete(raw, "tls")
	delete(raw, "users")

	// Nodes
	i.Nodes, _ = json.MarshalIndent(raw["nodes"], "", "  ")
	delete(raw, "nodes")

	// Addrs
	i.Addrs, _ = json.MarshalIndent(raw["addrs"], "", "  ")
	delete(raw, "addrs")

	// OutJson
	i.OutJson, _ = json.MarshalIndent(raw["out_json"], "", "  ")
	delete(raw, "out_json")

	// Remaining fields
	i.Options, err = json.MarshalIndent(raw, "", "  ")
	return err
}

// MarshalJSON customizes marshalling
func (i Inbound) MarshalJSON() ([]byte, error) {
	// Combine fixed fields and dynamic fields into one map
	combined := make(map[string]interface{})
	combined["type"] = i.Type
	combined["tag"] = i.Tag
	i.marshalTls(combined)

	if i.Options != nil {
		var restFields map[string]json.RawMessage
		if err := json.Unmarshal(i.Options, &restFields); err != nil {
			return nil, err
		}

		for k, v := range restFields {
			combined[k] = v
		}
	}

	return json.Marshal(combined)
}

func (i Inbound) MarshalFull() (*map[string]interface{}, error) {
	combined := make(map[string]interface{})
	combined["id"] = i.Id
	combined["type"] = i.Type
	combined["tag"] = i.Tag
	combined["tls_id"] = i.TlsId
	combined["nodes"] = i.Nodes
	combined["addrs"] = i.Addrs
	combined["out_json"] = i.OutJson

	if i.Options != nil {
		var restFields map[string]interface{}
		if err := json.Unmarshal(i.Options, &restFields); err != nil {
			return nil, err
		}

		for k, v := range restFields {
			combined[k] = v
		}
	}
	return &combined, nil
}

func (i Inbound) marshalTls(combined map[string]interface{}) {
	if i.Tls != nil {
		var server map[string]interface{}
		if err := json.Unmarshal(i.Tls.Server, &server); err == nil {
			if path, ok := server["certificate_path"].(string); ok && path != "" {
				if content, err := os.ReadFile(path); err == nil {
					sContent := strings.ReplaceAll(string(content), "\r\n", "\n")
					server["certificate"] = strings.Split(strings.TrimSpace(sContent), "\n")
					delete(server, "certificate_path")
				}
			}
			if path, ok := server["key_path"].(string); ok && path != "" {
				if content, err := os.ReadFile(path); err == nil {
					sContent := strings.ReplaceAll(string(content), "\r\n", "\n")
					server["key"] = strings.Split(strings.TrimSpace(sContent), "\n")
					delete(server, "key_path")
				}
			}
			combined["tls"] = server
		} else {
			combined["tls"] = i.Tls.Server
		}
	}
}
