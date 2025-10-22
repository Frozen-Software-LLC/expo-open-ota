package keyStore

import (
	"expo-open-ota/config"
	"fmt"
	"log"
	"time"
)

type KeysStorageType string

const (
	AWSSecretsManager KeysStorageType = "aws-secrets-manager"
	LocalFiles        KeysStorageType = "local-files"
	Environment       KeysStorageType = "environment"
)

type KeysStorage interface {
	GetPublicExpoKey() string
	GetPrivateExpoKey() string
	GetPrivateCloudfrontKey() string
}

func getStorage() (KeysStorage, error) {
	var storageType KeysStorageType
	if config.GetEnv("KEYS_STORAGE_TYPE") == "aws-secrets-manager" {
		storageType = AWSSecretsManager
	} else if config.GetEnv("KEYS_STORAGE_TYPE") == "local" {
		storageType = LocalFiles
	} else {
		storageType = Environment
	}

	switch storageType {
	case AWSSecretsManager:
		publicKeySecretID := config.GetEnv("AWSSM_EXPO_PUBLIC_KEY_SECRET_ID")
		privateKeySecretID := config.GetEnv("AWSSM_EXPO_PRIVATE_KEY_SECRET_ID")
		privateCloudfrontKeySecretID := config.GetEnv("AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID")
		if publicKeySecretID == "" || privateKeySecretID == "" {
			return nil, fmt.Errorf("PUBLIC_KEY_SECRET_ID, PRIVATE_KEY_SECRET_ID must be set in environment")
		}
		return &AWSSMKeysStorage{
			publicExpoKeySecretID:        publicKeySecretID,
			privateExpoKeySecretID:       privateKeySecretID,
			privateCloudfrontKeySecretID: privateCloudfrontKeySecretID,
		}, nil
	case LocalFiles:
		publicKeyPath := config.GetEnv("PUBLIC_LOCAL_EXPO_KEY_PATH")
		privateKeyPath := config.GetEnv("PRIVATE_LOCAL_EXPO_KEY_PATH")
		privateCloudfrontKeyPath := config.GetEnv("PRIVATE_CLOUDFRONT_KEY_PATH")
		if publicKeyPath == "" || privateKeyPath == "" {
			return nil, fmt.Errorf("PUBLIC_KEY_PATH and PRIVATE_KEY_PATH must be set in environment")
		}
		return &LocalKeysStorage{
			publicExpoKeyPath:        publicKeyPath,
			privateExpoKeyPath:       privateKeyPath,
			privateCloudfrontKeyPath: privateCloudfrontKeyPath,
		}, nil
	case Environment:
		return &EnvironmentKeysStorage{
			publicExpoKeyBase64Key:        "PUBLIC_EXPO_KEY_B64",
			privateExpoKeyBase64Key:       "PRIVATE_EXPO_KEY_B64",
			privateCloudfrontKeyBase64Key: "PRIVATE_CLOUDFRONT_KEY_B64",
		}, nil
	default:
		return nil, fmt.Errorf("unknown keyStore storage type: %s", storageType)
	}
}

func GetPublicExpoKey() string {
	storage, err := getStorage()
	if err != nil {
		return ""
	}
	return storage.GetPublicExpoKey()
}

func GetPrivateExpoKey() string {
	start := time.Now()
	log.Printf("[TRACE] GetPrivateExpoKey - START at %s", start.Format(time.RFC3339Nano))
	defer func() {
		log.Printf("[TRACE] GetPrivateExpoKey - END duration=%v", time.Since(start))
	}()

	storage, err := getStorage()
	if err != nil {
		return ""
	}
	return storage.GetPrivateExpoKey()
}

func GetPrivateCloudfrontKey() string {
	storage, err := getStorage()
	if err != nil {
		return ""
	}
	return storage.GetPrivateCloudfrontKey()
}
