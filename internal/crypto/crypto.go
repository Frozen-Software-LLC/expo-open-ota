package crypto

import (
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"log"
	"strings"
	"time"
)

func CreateHash(data []byte, hashingAlgorithm, encoding string) (string, error) {
	var h hash.Hash
	switch hashingAlgorithm {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	case "md5":
		h = md5.New()
	default:
		return "", fmt.Errorf("unsupported hashing algorithm: %s", hashingAlgorithm)
	}
	if _, err := h.Write(data); err != nil {
		return "", fmt.Errorf("unable to write data into hasher: %w", err)
	}
	sum := h.Sum(nil)
	switch encoding {
	case "hex":
		return hex.EncodeToString(sum), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(sum), nil
	default:
		return "", fmt.Errorf("unsupported encoding: %s", encoding)
	}
}

func ConvertSHA256HashToUUID(value string) string {
	if len(value) < 32 {
		return ""
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		value[:8],
		value[8:12],
		value[12:16],
		value[16:20],
		value[20:32],
	)
}

func GetBase64URLEncoding(encodedString string) string {
	base64EncodedString := strings.ReplaceAll(encodedString, "+", "-")
	base64EncodedString = strings.ReplaceAll(base64EncodedString, "/", "_")
	base64EncodedString = strings.TrimRight(base64EncodedString, "=")
	return base64EncodedString
}

func SignRSASHA256(data, privateKeyPEM string) (string, error) {
	start := time.Now()
	log.Printf("[TRACE] SignRSASHA256 - START at %s", start.Format(time.RFC3339Nano))
	defer func() {
		log.Printf("[TRACE] SignRSASHA256 - END duration=%v", time.Since(start))
	}()

	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("invalid private key PEM format")
	}
	var privateKey *rsa.PrivateKey
	var err error
	if privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
		parsedKey, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return "", fmt.Errorf("failed to parse private key: %w", parseErr)
		}
		var ok bool
		privateKey, ok = parsedKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("key is not an RSA private key")
		}
	}
	hashed := sha256.Sum256([]byte(data))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("failed to sign data: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func SignRSASHA1(data, privateKeyPEM string) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("invalid private key PEM format")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsedKey, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return "", fmt.Errorf("failed to parse private key: %w", parseErr)
		}
		privateKeyTmp, ok := parsedKey.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("key is not an RSA private key")
		}
		privateKey = privateKeyTmp
	}

	hashed := sha1.Sum([]byte(data))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA1, hashed[:])
	if err != nil {
		return "", fmt.Errorf("failed to sign data: %w", err)
	}

	return base64.StdEncoding.EncodeToString(signature), nil
}
