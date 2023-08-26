/*
 * SPDX-License-Identifier: AGPL-3.0-only
 * Copyright (c) 2022-2023, daeuniverse Organization <dae@v2raya.org>
 */

package quicutils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"golang.org/x/crypto/hkdf"
	"io"

	"github.com/wwqgtxx/wstunnel/fallback/ssaead"
)

const (
	MaxVarintLen64 = 8

	MaxPacketNumberLength = 4
	SampleSize            = 16
)

var (
	InitialClientLabel = []byte("client in")
)

type Keys struct {
	version             Version
	clientInitialSecret []byte
	key                 []byte
	iv                  []byte
	headerProtectionKey []byte
	newAead             func(key []byte) (cipher.AEAD, error)
}

func (k *Keys) Close() error {
	return nil
}

func NewKeys(clientDstConnectionId []byte, version Version, newAead func(key []byte) (cipher.AEAD, error)) (keys *Keys, err error) {
	// https://datatracker.ietf.org/doc/html/rfc9001#name-keys
	initialSecret := hkdf.Extract(sha256.New, clientDstConnectionId, version.InitialSalt())
	clientInitialSecret, err := HkdfExpandLabel(sha256.New, initialSecret, InitialClientLabel, nil, 32)
	if err != nil {
		return nil, err
	}

	keys = &Keys{
		clientInitialSecret: clientInitialSecret,
		version:             version,
		newAead:             newAead,
	}
	// We differentiated a deriveKeys func is just for example test.
	if err = keys.deriveKeys(); err != nil {
		keys.Close()
		return nil, err
	}

	return keys, nil
}

func (k *Keys) deriveKeys() (err error) {
	k.key, err = HkdfExpandLabel(sha256.New, k.clientInitialSecret, k.version.KeyLabel(), nil, 16)
	if err != nil {
		return err
	}
	k.iv, err = HkdfExpandLabel(sha256.New, k.clientInitialSecret, k.version.IvLabel(), nil, 12)
	if err != nil {
		return err
	}
	k.headerProtectionKey, err = HkdfExpandLabel(sha256.New, k.clientInitialSecret, k.version.HpLabel(), nil, 16)
	if err != nil {
		return err
	}
	return nil
}

// HeaderProtection encrypt/decrypt firstByte and packetNumber in place.
func (k *Keys) HeaderProtection(sample []byte, longHeader bool, firstByte *byte, potentialPacketNumber []byte) (packetNumber []byte, err error) {
	block, err := aes.NewCipher(k.headerProtectionKey)
	if err != nil {
		return nil, err
	}
	// Get mask.
	mask := make([]byte, block.BlockSize())
	block.Encrypt(mask, sample)
	// Encrypt/decrypt first byte.
	if longHeader {
		// Long header: 4 bits masked
		// High 4 bits are not protected.
		*firstByte ^= mask[0] & 0x0f
	} else {
		// Short header: 5 bits masked
		// High 3 bits are not protected.
		*firstByte ^= mask[0] & 0x1f
	}
	// The length of the Packet Number field is the value of this field plus one.
	packetNumberLength := int((*firstByte & 0b11) + 1)
	packetNumber = potentialPacketNumber[:packetNumberLength]

	// Encrypt/decrypt packet number.
	for i := range packetNumber {
		packetNumber[i] ^= mask[1+i]
	}
	return packetNumber, nil
}

func (k *Keys) PayloadDecrypt(ciphertext []byte, packetNumber []byte, header []byte) (plaintext []byte, err error) {
	// https://datatracker.ietf.org/doc/html/rfc9001#name-initial-secrets

	aead, err := k.newAead(k.key)
	if err != nil {
		return nil, err
	}
	// We only decrypt once, so we do not need to XOR it back.
	// https://github.com/quic-go/qtls-go1-20/blob/e132a0e6cb45e20ac0b705454849a11d09ba5a54/cipher_suites.go#L496
	for i := range packetNumber {
		k.iv[len(k.iv)-len(packetNumber)+i] ^= packetNumber[i]
	}
	plaintext = make([]byte, len(ciphertext)-aead.Overhead())
	plaintext, err = aead.Open(plaintext[:0], k.iv, ciphertext, header)
	if err != nil {
	}
	return plaintext, nil
}

func DecryptQuic(header []byte, blockEnd int, destConnId []byte) (plaintext []byte, err error) {
	_version := binary.BigEndian.Uint32(header[1:])
	version, err := ParseVersion(_version)
	if err != nil {
		return nil, err
	}
	keys, err := NewKeys(destConnId, version, ssaead.AeadCipher(aes.NewCipher, cipher.NewGCM))
	if err != nil {
		return nil, err
	}
	defer keys.Close()
	if blockEnd-len(header) < SampleSize {
		return nil, io.ErrUnexpectedEOF
	}
	// Sample 16B
	sample := header[len(header) : len(header)+SampleSize]

	// Decrypt header flag and packet number.
	var packetNumber []byte
	if packetNumber, err = keys.HeaderProtection(sample, true, &header[0], header[len(header)-MaxPacketNumberLength:]); err != nil {
		return nil, err
	}
	header = header[:len(header)-MaxPacketNumberLength+len(packetNumber)] // Correct header
	payload := header[len(header):blockEnd]                               // Correct payload

	plaintext, err = keys.PayloadDecrypt(payload, packetNumber, header)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}
