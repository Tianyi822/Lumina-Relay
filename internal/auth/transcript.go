package auth

import (
	"encoding/binary"
	"fmt"
)

const (
	DomainAccountCreate = "lumina-account-create"
	DomainLoginProof    = "lumina-login-proof"
	DomainDeviceSession = "lumina-device-session"
	DomainDiscardGroups = "lumina-discard-sync-groups"
	DomainDEKEnvelope   = "lumina-dek-envelope"
)

// BuildTranscript 使用 domain 原文加逐字段 uint32be 长度前缀，消除拼接歧义。
func BuildTranscript(domain string, fields ...[]byte) ([]byte, error) {
	if domain == "" {
		return nil, fmt.Errorf("transcript domain 不能为空")
	}
	out := []byte(domain)
	for _, field := range fields {
		if uint64(len(field)) > uint64(^uint32(0)) {
			return nil, fmt.Errorf("transcript 字段过长")
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		out = append(out, length[:]...)
		out = append(out, field...)
	}
	return out, nil
}

func BuildAccountCreateTranscript(
	instanceID, attemptID, username, accountID string,
	challenge, authSalt, loginPublicKey, accountAuthPublicKey, envelopeHash []byte,
	deviceID, deviceName string,
	devicePublicKey []byte,
) ([]byte, error) {
	return BuildTranscript(DomainAccountCreate,
		[]byte(instanceID), []byte(attemptID), challenge,
		[]byte(username), []byte(accountID), authSalt,
		loginPublicKey, accountAuthPublicKey, envelopeHash,
		[]byte(deviceID), []byte(deviceName), devicePublicKey,
	)
}

func BuildLoginTranscript(
	instanceID, attemptID, username string,
	challenge []byte,
	deviceID, deviceName string,
	devicePublicKey []byte,
) ([]byte, error) {
	return BuildTranscript(DomainLoginProof,
		[]byte(instanceID), []byte(attemptID), []byte(username), challenge,
		[]byte(deviceID), []byte(deviceName), devicePublicKey,
	)
}

func BuildSessionTranscript(
	instanceID, attemptID string,
	challenge []byte,
	deviceID string,
) ([]byte, error) {
	return BuildTranscript(DomainDeviceSession,
		[]byte(instanceID), []byte(attemptID), challenge, []byte(deviceID),
	)
}

func BuildDiscardGroupsTranscript(
	instanceID, accountID, deviceID, groupID string,
	groupRevision int64,
) ([]byte, error) {
	var revision [8]byte
	binary.BigEndian.PutUint64(revision[:], uint64(groupRevision))
	return BuildTranscript(DomainDiscardGroups,
		[]byte(instanceID), []byte(accountID), []byte(deviceID),
		[]byte(groupID), revision[:],
	)
}

// BuildDEKEnvelopeAAD 固定 XChaCha20-Poly1305 DEK envelope 的附加认证数据。
func BuildDEKEnvelopeAAD(
	instanceID, username, accountID string,
	authSalt []byte,
) ([]byte, error) {
	return BuildTranscript(DomainDEKEnvelope,
		[]byte(instanceID), []byte(username), []byte(accountID), authSalt)
}
