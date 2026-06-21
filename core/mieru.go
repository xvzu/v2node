package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	panel "github.com/wyx2685/v2node/api/v2board"
	"github.com/wyx2685/v2node/common/format"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/pbkdf2"
)

const (
	mieruKeyIter        = 64
	mieruKeyRefresh     = 2 * time.Minute
	mieruNonceLen       = 24
	mieruMetadataLen    = 32
	mieruAuthTagLen     = 16
	mieruMaxPDU         = 32768
	mieruSessionDataLen = 1024
)

const (
	mieruProtocolCloseConn       byte = 0
	mieruProtocolCloseConnResp   byte = 1
	mieruProtocolOpenSession     byte = 2
	mieruProtocolOpenSessionResp byte = 3
	mieruProtocolCloseSession    byte = 4
	mieruProtocolCloseSessionResp byte = 5
	mieruProtocolDataC2S         byte = 6
	mieruProtocolDataS2C         byte = 7
	mieruProtocolAckC2S          byte = 8
	mieruProtocolAckS2C          byte = 9
)

type MieruServer struct {
	mu       sync.Mutex
	listener net.Listener
	port     int
	users    map[string]*mieruUserState
	tag      string
	vc       *V2Core
	done     chan struct{}
	started  atomic.Bool

	sessions  map[uint32]*mieruSession
	sessionID atomic.Uint32
	sessionMu sync.Mutex
}

type mieruUserState struct {
	uuid      string
	hashedPwd []byte
}

type mieruSession struct {
	id         uint32
	userUUID   string
	conn       net.Conn
	sendCiph   *mieruCipher
	recvCiph   *mieruCipher
	upBytes    atomic.Int64
	downBytes  atomic.Int64
	done       chan struct{}
	writeMu    sync.Mutex
	writeCond  *sync.Cond
	closed     atomic.Bool
}

type mieruCipher struct {
	key          []byte
	nonce        [mieruNonceLen]byte
	nonceInc     uint64
	implicitMode bool
	mu           sync.Mutex
}

type mieruMetadata struct {
	protocolType byte
	sessionID    uint32
	sequenceNum  uint32
	payloadLen   uint16
	suffixLen    byte
	prefixLen    byte
}

func NewMieruServer(tag string, port int, vc *V2Core) *MieruServer {
	return &MieruServer{
		port:     port,
		tag:      tag,
		vc:       vc,
		users:    make(map[string]*mieruUserState),
		done:     make(chan struct{}),
		sessions: make(map[uint32]*mieruSession),
	}
}

func (ms *MieruServer) Start() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.started.Load() {
		return nil
	}
	addr := fmt.Sprintf(":%d", ms.port)
	var err error
	ms.listener, err = net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mieru listen error: %w", err)
	}
	ms.started.Store(true)
	log.WithField("tag", ms.tag).Infof("Mieru server started on %s", addr)
	go ms.acceptLoop()
	return nil
}

func (ms *MieruServer) Stop() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if !ms.started.Load() {
		return
	}
	ms.started.Store(false)
	close(ms.done)
	if ms.listener != nil {
		ms.listener.Close()
	}
	ms.sessionMu.Lock()
	for id, s := range ms.sessions {
		s.closed.Store(true)
		close(s.done)
		if s.conn != nil {
			s.conn.Close()
		}
		delete(ms.sessions, id)
	}
	ms.sessionMu.Unlock()
}

func (ms *MieruServer) IsRunning() bool {
	return ms.started.Load()
}

func (ms *MieruServer) SetUsers(users []panel.UserInfo) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	newUsers := make(map[string]*mieruUserState, len(users))
	for _, u := range users {
		pwd := append([]byte(u.Uuid), 0x00)
		pwd = append(pwd, []byte(u.Uuid)...)
		hp := sha256.Sum256(pwd)
		newUsers[u.Uuid] = &mieruUserState{
			uuid:      u.Uuid,
			hashedPwd: hp[:],
		}
	}
	ms.users = newUsers
}

func (ms *MieruServer) AddUser(uuid string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	pwd := append([]byte(uuid), 0x00)
	pwd = append(pwd, []byte(uuid)...)
	hp := sha256.Sum256(pwd)
	ms.users[uuid] = &mieruUserState{
		uuid:      uuid,
		hashedPwd: hp[:],
	}
}

func (ms *MieruServer) RemoveUser(uuid string) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	delete(ms.users, uuid)
}

func (ms *MieruServer) GetTraffic(tag string) []panel.UserTraffic {
	ms.sessionMu.Lock()
	defer ms.sessionMu.Unlock()
	trafficByUser := make(map[string]*panel.UserTraffic)
	for _, s := range ms.sessions {
		up := s.upBytes.Swap(0)
		down := s.downBytes.Swap(0)
		if up+down == 0 {
			continue
		}
		uid := 0
		ms.vc.users.mapLock.RLock()
		if id, ok := ms.vc.users.uidMap[format.UserTag(tag, s.userUUID)]; ok {
			uid = id
		}
		ms.vc.users.mapLock.RUnlock()
		if uid == 0 {
			continue
		}
		if t, ok := trafficByUser[s.userUUID]; ok {
			t.Upload += up
			t.Download += down
		} else {
			trafficByUser[s.userUUID] = &panel.UserTraffic{
				UID:      uid,
				Upload:   up,
				Download: down,
			}
		}
	}
	result := make([]panel.UserTraffic, 0, len(trafficByUser))
	for _, t := range trafficByUser {
		result = append(result, *t)
	}
	return result
}

func (ms *MieruServer) acceptLoop() {
	for {
		conn, err := ms.listener.Accept()
		if err != nil {
			select {
			case <-ms.done:
				return
			default:
				log.WithField("tag", ms.tag).Errorf("Mieru accept error: %s", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}
		go ms.handleConn(conn)
	}
}

func (ms *MieruServer) handleConn(conn net.Conn) {
	defer conn.Close()

	ms.mu.Lock()
	userMap := ms.users
	ms.mu.Unlock()

	var session *mieruSession

	first := true
	recvCiph := newMieruCipher(nil, true)
	sendCiph := newMieruCipher(nil, true)

	for {
		select {
		case <-ms.done:
			return
		default:
		}

		if first {
			nonce := make([]byte, mieruNonceLen)
			if _, err := io.ReadFull(conn, nonce); err != nil {
				return
			}

			encMeta := make([]byte, mieruMetadataLen+mieruAuthTagLen)
			if _, err := io.ReadFull(conn, encMeta); err != nil {
				return
			}

			var matched bool
			for _, state := range userMap {
				meta, ok := ms.tryDecryptSession(nonce, encMeta, state.hashedPwd)
				if !ok {
					continue
				}
				if meta.protocolType != mieruProtocolOpenSession {
					continue
				}

				sendKey := make([]byte, 32)
				rand.Read(sendKey)

				recvCiph = newMieruCipher(state.hashedPwd, true)
				recvCiph.setNonce(nonce)
				sendCiph = newMieruCipher(sendKey, true)
				sendCiph.randomizeNonce()

				sid := ms.sessionID.Add(1)
				session = &mieruSession{
					id:       sid,
					userUUID: state.uuid,
					conn:     conn,
					sendCiph: sendCiph,
					recvCiph: recvCiph,
					done:     make(chan struct{}),
				}

				ms.sessionMu.Lock()
				ms.sessions[sid] = session
				ms.sessionMu.Unlock()

				resp := marshalSessionMeta(mieruProtocolOpenSessionResp, sid, 0, 0)
				encResp, err := sendCiph.encrypt(resp)
				if err != nil {
					ms.removeSession(session)
					return
				}
				if _, err := conn.Write(encResp); err != nil {
					ms.removeSession(session)
					return
				}

				first = false
				matched = true
				break
			}
			if !matched {
				return
			}
			continue
		}

		meta, payload, err := ms.readSegment(conn, recvCiph)
		if err != nil {
			ms.removeSession(session)
			return
		}

		switch meta.protocolType {
		case mieruProtocolCloseSession:
			resp := marshalSessionMeta(mieruProtocolCloseSessionResp, meta.sessionID, 0, 0)
			if encResp, err := sendCiph.encrypt(resp); err == nil {
				conn.Write(encResp)
			}
			ms.removeSession(session)
			return

		case mieruProtocolCloseConn:
			ms.removeSession(session)
			return

		case mieruProtocolDataC2S:
			if session == nil || len(payload) == 0 {
				continue
			}
			session.downBytes.Add(int64(len(payload)))
			ms.proxyPayload(session, conn, payload)

		case mieruProtocolDataS2C, mieruProtocolAckC2S, mieruProtocolAckS2C:

		}
	}
}

func (ms *MieruServer) readSegment(conn net.Conn, ciph *mieruCipher) (*mieruMetadata, []byte, error) {
	encMeta := make([]byte, mieruMetadataLen+mieruAuthTagLen)
	if _, err := io.ReadFull(conn, encMeta); err != nil {
		return nil, nil, err
	}
	metaBytes, err := ciph.decrypt(encMeta)
	if err != nil {
		return nil, nil, err
	}
	meta := parseMetadata(metaBytes)

	if meta.payloadLen > 0 {
		if meta.prefixLen > 0 {
			pref := make([]byte, meta.prefixLen)
			if _, err := io.ReadFull(conn, pref); err != nil {
				return meta, nil, err
			}
		}
		encPayload := make([]byte, int(meta.payloadLen)+mieruAuthTagLen)
		if _, err := io.ReadFull(conn, encPayload); err != nil {
			return meta, nil, err
		}
		payload, err := ciph.decrypt(encPayload)
		if err != nil {
			return meta, nil, err
		}
		if meta.suffixLen > 0 {
			suff := make([]byte, meta.suffixLen)
			io.ReadFull(conn, suff)
		}
		return meta, payload, nil
	}

	if meta.suffixLen > 0 {
		suff := make([]byte, meta.suffixLen)
		io.ReadFull(conn, suff)
	}
	return meta, nil, nil
}

func (ms *MieruServer) proxyPayload(session *mieruSession, conn net.Conn, data []byte) {
	dest, payload, err := parseSOCKS5Connect(data)
	if err != nil || dest == "" {
		return
	}

	target, err := net.DialTimeout("tcp", dest, 10*time.Second)
	if err != nil {
		return
	}

	if len(payload) > 0 {
		target.Write(payload)
	}

	done := make(chan struct{})

	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := target.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				session.upBytes.Add(int64(n))
				ms.sendDataSegment(session, conn, chunk)
			}
			if err != nil {
				close(done)
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-session.done:
			target.Close()
			return
		default:
		}

		meta, payload, err := ms.readSegment(conn, session.recvCiph)
		if err != nil {
			target.Close()
			return
		}
		if meta.protocolType == mieruProtocolCloseSession ||
			meta.protocolType == mieruProtocolCloseConn {
			target.Close()
			return
		}
		if meta.protocolType == mieruProtocolDataC2S && len(payload) > 0 {
			session.downBytes.Add(int64(len(payload)))
			target.Write(payload)
		}
	}
}

func (ms *MieruServer) sendDataSegment(session *mieruSession, conn net.Conn, data []byte) {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()

	meta := &mieruMetadata{
		protocolType: mieruProtocolDataS2C,
		sessionID:    session.id,
		payloadLen:   uint16(len(data)),
	}
	metaBytes := marshalMetadata(meta)
	encMeta, err := session.sendCiph.encrypt(metaBytes)
	if err != nil {
		return
	}
	encPayload, err := session.sendCiph.encrypt(data)
	if err != nil {
		return
	}
	segment := make([]byte, 0, len(encMeta)+len(encPayload))
	segment = append(segment, encMeta...)
	segment = append(segment, encPayload...)
	conn.Write(segment)
}

func (ms *MieruServer) tryDecryptSession(nonce, encMeta, hashedPwd []byte) (*mieruMetadata, bool) {
	now := time.Now()
	for i := -1; i <= 1; i++ {
		t := now.Add(time.Duration(i) * mieruKeyRefresh)
		salt := mieruSaltFromTime(t)
		key := pbkdf2.Key(hashedPwd, salt, mieruKeyIter, 32, sha256.New)

		ciph, err := chacha20poly1305.NewX(key)
		if err != nil {
			continue
		}
		plaintext, err := ciph.Open(nil, nonce, encMeta, nil)
		if err != nil {
			continue
		}
		if len(plaintext) < mieruMetadataLen {
			continue
		}
		meta := parseMetadata(plaintext[:mieruMetadataLen])
		if meta.protocolType == mieruProtocolOpenSession {
			return meta, true
		}
	}
	return nil, false
}

func mieruSaltFromTime(t time.Time) []byte {
	unix := t.Unix()
	rounded := unix - (unix % 120)
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(rounded))
	h := sha256.Sum256(b)
	return h[:]
}

func newMieruCipher(key []byte, implicit bool) *mieruCipher {
	c := &mieruCipher{
		key:          make([]byte, 32),
		implicitMode: implicit,
	}
	copy(c.key, key)
	return c
}

func (c *mieruCipher) setNonce(nonce []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy(c.nonce[:], nonce)
	c.nonceInc = 0
}

func (c *mieruCipher) randomizeNonce() {
	c.mu.Lock()
	defer c.mu.Unlock()
	rand.Read(c.nonce[:])
	c.nonceInc = 0
}

func (c *mieruCipher) encrypt(plaintext []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	aead, err := chacha20poly1305.NewX(c.key)
	if err != nil {
		return nil, err
	}

	if c.implicitMode && c.nonceInc == 0 {
		c.randomizeNonce()
	}

	var nonce [mieruNonceLen]byte
	if c.implicitMode {
		copy(nonce[:], c.nonce[:])
		nonceInt := binary.BigEndian.Uint64(nonce[16:24])
		nonceInt += c.nonceInc
		binary.BigEndian.PutUint64(nonce[16:24], nonceInt)
	} else {
		clearNonce := make([]byte, mieruNonceLen)
		rand.Read(clearNonce)
		copy(nonce[:], clearNonce)
	}

	ciphertext := aead.Seal(nil, nonce[:], plaintext, nil)

	result := ciphertext

	if c.implicitMode && c.nonceInc == 0 {
		result = append(c.nonce[:], ciphertext...)
	}
	c.nonceInc++
	return result, nil
}

func (c *mieruCipher) decrypt(ciphertext []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	aead, err := chacha20poly1305.NewX(c.key)
	if err != nil {
		return nil, err
	}

	var nonce [mieruNonceLen]byte

	if c.implicitMode && c.nonceInc == 0 {
		if len(ciphertext) < mieruNonceLen {
			return nil, io.ErrUnexpectedEOF
		}
		copy(c.nonce[:], ciphertext[:mieruNonceLen])
		copy(nonce[:], c.nonce[:])
		ciphertext = ciphertext[mieruNonceLen:]
	} else {
		copy(nonce[:], c.nonce[:])
		nonceInt := binary.BigEndian.Uint64(nonce[16:24])
		nonceInt += c.nonceInc
		binary.BigEndian.PutUint64(nonce[16:24], nonceInt)
	}

	c.nonceInc++
	return aead.Open(nil, nonce[:], ciphertext, nil)
}

func parseMetadata(data []byte) *mieruMetadata {
	meta := &mieruMetadata{}
	if len(data) < 12 {
		return meta
	}
	meta.protocolType = data[0]
	meta.sessionID = binary.BigEndian.Uint32(data[2:6])
	meta.sequenceNum = binary.BigEndian.Uint32(data[6:10])
	meta.payloadLen = binary.BigEndian.Uint16(data[10:12])
	if len(data) > 13 {
		meta.prefixLen = data[12]
		meta.suffixLen = data[13]
	}
	return meta
}

func marshalMetadata(meta *mieruMetadata) []byte {
	b := make([]byte, mieruMetadataLen)
	b[0] = meta.protocolType
	binary.BigEndian.PutUint32(b[2:6], meta.sessionID)
	binary.BigEndian.PutUint32(b[6:10], meta.sequenceNum)
	binary.BigEndian.PutUint16(b[10:12], meta.payloadLen)
	return b
}

func marshalSessionMeta(ptype byte, sid, seq uint32, payLen uint16) []byte {
	return marshalMetadata(&mieruMetadata{
		protocolType: ptype,
		sessionID:    sid,
		sequenceNum:  seq,
		payloadLen:   payLen,
	})
}

func parseSOCKS5Connect(data []byte) (string, []byte, error) {
	if len(data) < 3 {
		return "", nil, io.ErrUnexpectedEOF
	}

	if data[0] != 0x05 {
		return "", data, nil
	}

	if len(data) < 4 || data[1] != 0x01 {
		return "", nil, nil
	}

	addrType := data[3]
	var host string
	var port int
	var offset int

	switch addrType {
	case 0x01:
		if len(data) < 10 {
			return "", nil, io.ErrUnexpectedEOF
		}
		host = net.IP(data[4:8]).String()
		port = int(binary.BigEndian.Uint16(data[8:10]))
		offset = 10
	case 0x03:
		if len(data) < 5 {
			return "", nil, io.ErrUnexpectedEOF
		}
		addrLen := int(data[4])
		if len(data) < 5+addrLen+2 {
			return "", nil, io.ErrUnexpectedEOF
		}
		host = string(data[5 : 5+addrLen])
		port = int(binary.BigEndian.Uint16(data[5+addrLen : 5+addrLen+2]))
		offset = 5 + addrLen + 2
	case 0x04:
		if len(data) < 22 {
			return "", nil, io.ErrUnexpectedEOF
		}
		host = net.IP(data[4:20]).String()
		port = int(binary.BigEndian.Uint16(data[20:22]))
		offset = 22
	default:
		return "", nil, io.ErrUnexpectedEOF
	}

	dest := fmt.Sprintf("%s:%d", host, port)
	var payload []byte
	if offset < len(data) {
		payload = data[offset:]
	}
	return dest, payload, nil
}

func (ms *MieruServer) removeSession(s *mieruSession) {
	if s == nil {
		return
	}
	s.closed.Store(true)
	close(s.done)
	ms.sessionMu.Lock()
	delete(ms.sessions, s.id)
	ms.sessionMu.Unlock()
}
