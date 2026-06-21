package core

import (
	"fmt"
	"sync"

	panel "github.com/wyx2685/v2node/api/v2board"
)

type MieruManager struct {
	mu      sync.Mutex
	servers map[string]*MieruServer
}

var Mieru = &MieruManager{
	servers: make(map[string]*MieruServer),
}

func (v *V2Core) AddNode(tag string, info *panel.NodeInfo) error {
	if info.Type == "mieru" {
		ms := NewMieruServer(tag, info.Common.ServerPort, v)
		Mieru.mu.Lock()
		Mieru.servers[tag] = ms
		Mieru.mu.Unlock()
		return ms.Start()
	}

	inBoundConfig, err := buildInbound(info, tag)
	if err != nil {
		return fmt.Errorf("build inbound error: %s", err)
	}
	err = v.addInbound(inBoundConfig)
	if err != nil {
		return fmt.Errorf("add inbound error: %s", err)
	}
	return nil
}

func (v *V2Core) DelNode(tag string) error {
	Mieru.mu.Lock()
	if ms, ok := Mieru.servers[tag]; ok {
		delete(Mieru.servers, tag)
		Mieru.mu.Unlock()
		ms.Stop()
		return nil
	}
	Mieru.mu.Unlock()

	err := v.removeInbound(tag)
	if err != nil {
		return fmt.Errorf("remove in error: %s", err)
	}
	return nil
}

func (v *V2Core) GetMieruServer(tag string) *MieruServer {
	Mieru.mu.Lock()
	defer Mieru.mu.Unlock()
	return Mieru.servers[tag]
}
