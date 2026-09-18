package server

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"houdry/internal/gpu"
	"houdry/internal/pki"
)

type enrollRequest struct {
	Token  string `json:"token"`
	CSR    string `json:"csr"`
	NodeID string `json:"node_id"`
}

type enrollResponse struct {
	Certificate string `json:"certificate"`
	CA          string `json:"ca"`
	Node        Node   `json:"node"`
}

type ipLimiter struct {
	mu      sync.Mutex
	hits    map[string][]time.Time
	max     int
	window  time.Duration
}

func newIPLimiter(max int) *ipLimiter {
	if max <= 0 {
		max = 5
	}
	return &ipLimiter{hits: map[string][]time.Time{}, max: max, window: time.Minute}
}

func (l *ipLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-l.window)
	cur := l.hits[ip]
	n := cur[:0]
	for _, t := range cur {
		if t.After(cut) {
			n = append(n, t)
		}
	}
	if len(n) >= l.max {
		l.hits[ip] = n
		return false
	}
	l.hits[ip] = append(n, now)
	return true
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.enrollLimit != nil && !s.enrollLimit.allow(ip) {
		s.audit.Log("ENROLLMENT_FAILURE", "", "", ip, "rate limited")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many enrollment attempts"})
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req enrollRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	s.audit.Log("CSR_RECEIVED", req.NodeID, "", ip, "")
	if req.NodeID == "" || req.CSR == "" {
		s.audit.Log("ENROLLMENT_FAILURE", req.NodeID, "", ip, "missing fields")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id and csr are required"})
		return
	}
	if s.enroll == nil || s.bundle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "enrollment not configured"})
		return
	}
	csr, err := pki.ParseCSR([]byte(req.CSR))
	if err != nil {
		s.audit.Log("ENROLLMENT_FAILURE", req.NodeID, "", ip, err.Error())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := csr.CheckSignature(); err != nil {
		s.audit.Log("ENROLLMENT_FAILURE", req.NodeID, "", ip, err.Error())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid CSR signature"})
		return
	}
	if csr.Subject.CommonName != req.NodeID {
		s.audit.Log("ENROLLMENT_FAILURE", req.NodeID, "", ip, "csr cn mismatch")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "CSR CN does not match node_id"})
		return
	}
	if existing, ok := s.store.Get(req.NodeID); ok && existing.CertSerial != "" && existing.Identity != IdentityRevoked {
		s.audit.Log("ENROLLMENT_FAILURE", req.NodeID, existing.CertFingerprint, ip, "already enrolled")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "node already enrolled"})
		return
	}
	if err := s.enroll.Consume(req.Token, req.NodeID); err != nil {
		s.audit.Log("ENROLLMENT_FAILURE", req.NodeID, "", ip, err.Error())
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	cert, err := s.bundle.SignCSR([]byte(req.CSR), req.NodeID, s.certLifetime)
	if err != nil {
		s.audit.Log("ENROLLMENT_FAILURE", req.NodeID, "", ip, err.Error())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	n := Node{
		Inventory: gpu.Inventory{
			NodeID: req.NodeID,
		},
		Identity:        IdentityCertified,
		CertFingerprint: pki.FingerprintSHA256(cert),
		CertSerial:      pki.SerialHex(cert),
		CertNotAfter:    cert.NotAfter.UTC(),
		RemoteIP:        ip,
		Status:          StatusJoined,
	}
	n = s.store.Upsert(n)
	s.audit.Log("ENROLLMENT_SUCCESS", req.NodeID, n.CertFingerprint, ip, "")
	s.audit.Log("NODE_CERTIFICATE_ISSUED", req.NodeID, n.CertFingerprint, ip, "")
	writeJSON(w, http.StatusOK, enrollResponse{
		Certificate: string(pki.EncodeCertPEM(cert)),
		CA:          string(s.bundle.CACertPEM()),
		Node:        n,
	})
}

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := s.requireNodeCert(w, r, true)
	if !ok {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req struct {
		CSR string `json:"csr"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.CSR == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "csr is required"})
		return
	}
	cert, err := s.bundle.SignCSR([]byte(req.CSR), nodeID, s.certLifetime)
	if err != nil {
		s.audit.Log("ENROLLMENT_FAILURE", nodeID, "", clientIP(r), err.Error())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	n, _ := s.store.Get(nodeID)
	n.CertFingerprint = pki.FingerprintSHA256(cert)
	n.CertSerial = pki.SerialHex(cert)
	n.CertNotAfter = cert.NotAfter.UTC()
	if n.Identity == IdentityCertified {
		n.Identity = IdentityActive
	}
	n = s.store.Upsert(n)
	s.audit.Log("CERTIFICATE_RENEWED", nodeID, n.CertFingerprint, clientIP(r), "")
	writeJSON(w, http.StatusOK, enrollResponse{
		Certificate: string(pki.EncodeCertPEM(cert)),
		CA:          string(s.bundle.CACertPEM()),
		Node:        n,
	})
}

func (s *Server) handleCA(w http.ResponseWriter, r *http.Request) {
	if s.bundle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pki not ready"})
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.bundle.CACertPEM())
}

func (s *Server) handleAdminIdentity(w http.ResponseWriter, r *http.Request, identity string) {
	if !s.authorized(w, r) {
		return
	}
	id, ok := readNodeID(w, r)
	if !ok {
		return
	}
	n, found := s.store.Get(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not registered"})
		return
	}
	if identity == IdentityRevoked && n.CertSerial != "" && s.revoke != nil {
		_ = s.revoke.Revoke(n.CertSerial, id)
		s.audit.Log("CERTIFICATE_REVOKED", id, n.CertFingerprint, clientIP(r), "")
	}
	n, _ = s.store.SetIdentity(id, identity)
	writeJSON(w, http.StatusOK, n)
}
