package network

import (
	"errors"
	"net"
	"sync"

	"github.com/chrislonng/starx/log"
	"github.com/chrislonng/starx/message"
	"github.com/chrislonng/starx/packet"
	"github.com/chrislonng/starx/session"
)

var ErrSessionOnNotify = errors.New("current session working on notify mode")

var (
	heartbeatPacket, _ = packet.Pack(&packet.Packet{Type: packet.Heartbeat})
	defaultNetService  = NewNetService()
)

type netService struct {
	agentUidLock       sync.RWMutex             // protect agentUid
	agentUid           uint64                   // agent unique id
	agentMapLock       sync.RWMutex             // protect agentMap
	agentMap           map[uint64]*agent        // agents map
	acceptorUidLock    sync.RWMutex             // protect acceptorUid
	acceptorUid        uint64                   // acceptor unique id
	acceptorMapLock    sync.RWMutex             // protect acceptorMap
	acceptorMap        map[uint64]*acceptor     // acceptor map
	sessionCloseCbLock sync.RWMutex             // protect sessionCloseCb
	sessionCloseCb     []func(*session.Session) // callback on session closed
}

// Create new netservive
func NewNetService() *netService {
	return &netService{
		agentUid:    1,
		agentMap:    make(map[uint64]*agent),
		acceptorUid: 1,
		acceptorMap: make(map[uint64]*acceptor),
	}
}

// Create agent via netService
func (net *netService) createAgent(conn net.Conn) *agent {
	net.agentUidLock.Lock()
	id := net.agentUid
	net.agentUid++
	net.agentUidLock.Unlock()
	a := newAgent(id, conn)
	// add to maps
	net.agentMapLock.Lock()
	net.agentMap[id] = a
	net.agentMapLock.Unlock()
	return a
}

// get agent by session id
func (net *netService) getAgent(sid uint64) (*agent, error) {
	if a, ok := net.agentMap[sid]; ok && a != nil {
		return a, nil
	} else {
		return nil, errors.New("agent id: " + string(sid) + " not exists!")
	}
}

// Create acceptor via netService
func (net *netService) createAcceptor(conn net.Conn) *acceptor {
	net.acceptorUidLock.Lock()
	id := net.acceptorUid
	net.acceptorUid++
	net.acceptorUidLock.Unlock()
	a := newAcceptor(id, conn)
	// add to maps
	net.acceptorMapLock.Lock()
	net.acceptorMap[id] = a
	net.acceptorMapLock.Unlock()
	return a
}

func (net *netService) getAcceptor(sid uint64) (*acceptor, error) {
	if rs, ok := net.acceptorMap[sid]; ok && rs != nil {
		return rs, nil
	} else {
		return nil, errors.New("acceptor id: " + string(sid) + " not exists!")
	}
}

// Send packet data, call by package internal, the second argument was packaged packet
// if current server is frontend server, send to client by agent, else send to frontend
// server by acceptor
func (net *netService) send(session *session.Session, data []byte) {
	session.Entity.Send(data)
}

// Push message to client
// call by all package, the last argument was packaged message
func (net *netService) Push(session *session.Session, route string, data []byte) error {
	m, err := message.Encode(&message.Message{Type: message.MessageType(message.Push), Route: route, Data: data})
	if err != nil {
		log.Error(err.Error())
		return err
	}
	p := packet.Packet{
		Type:   packet.Data,
		Length: len(m),
		Data:   m,
	}
	ep, err := p.Pack()
	if err != nil {
		log.Error(err.Error())
		return err
	}
	net.send(session, ep)
	return nil
}

// Response message to client
// call by all package, the last argument was packaged message
func (net *netService) Response(session *session.Session, data []byte) error {
	// current message is notify message, can not response
	if session.LastID <= 0 {
		return ErrSessionOnNotify
	}
	m, err := message.Encode(&message.Message{
		Type: message.MessageType(message.Response),
		ID:   session.LastID,
		Data: data,
	})
	if err != nil {
		log.Error(err.Error())
		return err
	}
	p := packet.Packet{
		Type:   packet.Data,
		Length: len(m),
		Data:   m,
	}
	ep, err := p.Pack()
	if err != nil {
		log.Error(err.Error())
		return err
	}
	net.send(session, ep)
	return nil
}

// Broadcast message to all sessions
// Message level method
// call by all package, the last argument was packaged message
func (net *netService) Broadcast(route string, data []byte) {
	if appConfig.IsFrontend {
		for _, s := range net.agentMap {
			net.Push(s.session, route, data)
		}
	}
}

// Multicast message to special agent ids
func (net *netService) Multicast(aids []uint64, route string, data []byte) {
	for _, aid := range aids {
		if agent, ok := net.agentMap[aid]; ok && agent != nil {
			net.Push(agent.session, route, data)
		}
	}
}

// Close session
func (net *netService) closeSession(session *session.Session) {
	// TODO: notify all backend server, current session has closed.
	// session close callback
	net.sessionCloseCbLock.RLock()
	if len(net.sessionCloseCb) > 0 {
		for _, cb := range net.sessionCloseCb {
			if cb != nil {
				cb(session)
			}
		}
	}
	net.sessionCloseCbLock.RUnlock()
	if appConfig.IsFrontend {
		net.agentMapLock.Lock()
		if agent, ok := net.agentMap[session.Entity.ID()]; ok && (agent != nil) {
			delete(net.agentMap, session.Entity.ID())
		}
		net.agentMapLock.Unlock()
		defaultNetService.dumpAgents()
	} /* else {
		net.acceptorMapLock.RLock()
		if acceptor, ok := net.acceptorMap[session.entityID]; ok && (acceptor != nil) {
			// TODO: FIXED IT
			// backend session close should not cause acceptor remove from acceptor map
		}
		net.acceptorMapLock.RUnlock()
		defaultNetService.dumpAcceptor()
	}*/
}

func (net *netService) removeAcceptor(a *acceptor) {
	net.acceptorMapLock.Lock()
	delete(net.acceptorMap, a.id)
	net.acceptorMapLock.Unlock()
}

// Send heartbeat packet
func (net *netService) heartbeat() {
	if !appConfig.IsFrontend || net.agentMap == nil {
		return
	}
	for _, session := range net.agentMap {
		if session.status == statusWorking {
			session.send(heartbeatPacket)
			session.heartbeat()
		}
	}
}

// Dump all agents
func (net *netService) dumpAgents() {
	net.agentMapLock.RLock()
	defer net.agentMapLock.RUnlock()
	log.Info("current agent count: %d", len(net.agentMap))
	for _, ses := range net.agentMap {
		log.Info("session: " + ses.String())
	}
}

// Dump all acceptor
func (net *netService) dumpAcceptor() {
	net.acceptorMapLock.RLock()
	defer net.acceptorMapLock.RUnlock()
	log.Info("current acceptor count: %d", len(net.acceptorMap))
	for _, ses := range net.acceptorMap {
		log.Info("session: " + ses.String())
	}
}

func (net *netService) sessionClosedCallback(cb func(*session.Session)) {
	net.sessionCloseCbLock.Lock()
	defer net.sessionCloseCbLock.Unlock()
	net.sessionCloseCb = append(net.sessionCloseCb, cb)
}

// Callback when session closed
// Waring: session has closed,
func OnSessionClosed(cb func(*session.Session)) {
	defaultNetService.sessionClosedCallback(cb)
}