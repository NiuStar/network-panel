package controller

import (
	"reflect"
	"testing"

	"network-panel/golang-backend/internal/app/model"
)

func TestNodeConnectionStatePreservesConcurrentNodeEdits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		version string
	}{
		{"connect", 1, "go-agent-new"},
		{"connect_without_version", 1, ""},
		{"disconnect", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTunnelExternalTestDB(t)
			status := 1
			staleNode := model.Node{BaseEntity: model.BaseEntity{Status: &status, CreatedTime: 10, UpdatedTime: 20}, Name: "old-name", Secret: "old-secret", IP: "23.146.0.1", ServerIP: "23.146.0.1", Version: "go-agent-old", PortSta: 1000, PortEnd: 2000}
			if err := db.Create(&staleNode).Error; err != nil {
				t.Fatal(err)
			}
			// An operator edits the row after this websocket captured its snapshot.
			if err := db.Model(&model.Node{}).Where("id = ?", staleNode.ID).Updates(map[string]interface{}{"ip": "23.141.0.1", "server_ip": "23.141.0.1", "name": "current-name", "secret": "current-secret", "version": "go-agent-current", "port_sta": 3000, "updated_time": 30}).Error; err != nil {
				t.Fatal(err)
			}
			var want model.Node
			if err := db.First(&want, staleNode.ID).Error; err != nil {
				t.Fatal(err)
			}
			want.Status = &tc.status
			if tc.version != "" {
				want.Version = tc.version
			}
			if err := updateNodeConnectionState(staleNode.ID, tc.status, tc.version); err != nil {
				t.Fatal(err)
			}
			var got model.Node
			if err := db.First(&got, staleNode.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("connection update overwrote concurrent edits: got %+v; want %+v", got, want)
			}
		})
	}
}

func TestNodeConnectionStateRejectsInvalidID(t *testing.T) {
	for _, id := range []int64{0, -1} {
		if err := updateNodeConnectionState(id, 0, ""); err == nil {
			t.Fatalf("accepted invalid node ID %d", id)
		}
	}
}
