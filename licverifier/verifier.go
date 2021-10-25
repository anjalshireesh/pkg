// Copyright (c) 2015-2021 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// Package licverifier implements a simple library to verify MinIO Subnet license keys.
package licverifier

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/minio/pkg/env"
)

// LicenseVerifier needs an ECDSA public key in PEM format for initialization.
type LicenseVerifier struct {
	keySet jwk.Set
}

// LicenseInfo holds customer metadata present in the license key.
type LicenseInfo struct {
	Email           string    // Email of the license key requestor
	Organization    string    // Subnet organization name
	AccountID       int64     // Subnet account id
	DeploymentID    string    // Cluster deployment ID
	StorageCapacity int64     // Storage capacity used in TB
	Plan            string    // Subnet plan
	ExpiresAt       time.Time // Time of license expiry
}

// license key JSON field names
const (
	accountID    = "aid"
	deploymentID = "did"
	organization = "org"
	capacity     = "cap"
	plan         = "plan"
)

// parse PEM encoded PKCS1 or PKCS8 public key
func parseECPublicKeyFromPEM(key []byte) (*ecdsa.PublicKey, error) {
	var err error

	// Parse PEM block
	var block *pem.Block
	if block, _ = pem.Decode(key); block == nil {
		return nil, errors.New("key must be a PEM encoded PKCS1 or PKCS8 key")
	}

	// Parse the key
	var parsedKey interface{}
	if parsedKey, err = x509.ParsePKIXPublicKey(block.Bytes); err != nil {
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			parsedKey = cert.PublicKey
		} else {
			return nil, err
		}
	}

	var pkey *ecdsa.PublicKey
	var ok bool
	if pkey, ok = parsedKey.(*ecdsa.PublicKey); !ok {
		return nil, errors.New("key is not a valid RSA public key")
	}

	return pkey, nil
}

// NewLicenseVerifier returns an initialized license verifier with the given
// ECDSA public key in PEM format.
func NewLicenseVerifier(pemBytes []byte) (*LicenseVerifier, error) {
	pbKey, err := parseECPublicKeyFromPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse public key: %s", err)
	}
	key, err := jwk.New(pbKey)
	if err != nil {
		return nil, err
	}
	key.Set(jwk.AlgorithmKey, jwa.ES384)
	keyset := jwk.NewSet()
	keyset.Add(key)
	return &LicenseVerifier{
		keySet: keyset,
	}, nil
}

// toLicenseInfo extracts LicenseInfo from claims. It returns an error if any of
// the claim values are invalid.
func toLicenseInfo(token jwt.Token) (LicenseInfo, error) {
	claims, err := token.AsMap(context.Background())
	if err != nil {
		return LicenseInfo{}, err
	}
	accID, ok := claims[accountID].(float64)
	if !ok || ok && accID < 0 {
		return LicenseInfo{}, errors.New("Invalid accountId in claims")
	}

	// deployment id may not be present in older licenses.
	// so don't fail if it's not found.
	depUUID, _ := claims[deploymentID].(string)

	orgName, ok := claims[organization].(string)
	if !ok {
		return LicenseInfo{}, errors.New("Invalid organization in claims")
	}
	storageCap, ok := claims[capacity].(float64)
	if !ok {
		return LicenseInfo{}, errors.New("Invalid storage capacity in claims")
	}
	plan, ok := claims[plan].(string)
	if !ok {
		return LicenseInfo{}, errors.New("Invalid plan in claims")
	}
	return LicenseInfo{
		Email:           token.Subject(),
		Organization:    orgName,
		AccountID:       int64(accID),
		DeploymentID:    depUUID,
		StorageCapacity: int64(storageCap),
		Plan:            plan,
		ExpiresAt:       token.Expiration(),
	}, nil

}

// Verify verifies the license key and validates the claims present in it.
func (lv *LicenseVerifier) Verify(license string, options ...jwt.ParseOption) (LicenseInfo, error) {
	options = append(options, jwt.WithKeySet(lv.keySet), jwt.UseDefaultKey(true), jwt.WithValidate(true))
	token, err := jwt.ParseString(license, options...)
	if err != nil {
		return LicenseInfo{}, fmt.Errorf("failed to verify license: %s", err)
	}

	return toLicenseInfo(token)
}

// VerifyClusterLicense - verifies if the given license string is valid for the given cluster (deployment id)
func VerifyClusterLicense(lic string, depID string, options ...jwt.ParseOption) error {
	var pemBytes []byte

	devMode := env.Get("MINIO_CI_CD", "") != ""
	if devMode {
		pemBytes = []byte(`-----BEGIN PUBLIC KEY-----
MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAEbo+e1wpBY4tBq9AONKww3Kq7m6QP/TBQ
mr/cKCUyBL7rcAvg0zNq1vcSrUSGlAmY3SEDCu3GOKnjG/U4E7+p957ocWSV+mQU
9NKlTdQFGF3+aO6jbQ4hX/S5qPyF+a3z
-----END PUBLIC KEY-----`)
	} else {
		pemBytes = []byte(`-----BEGIN PUBLIC KEY-----
MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAEaK31xujr6/rZ7ZfXZh3SlwovjC+X8wGq
qkltaKyTLRENd4w3IRktYYCRgzpDLPn/nrf7snV/ERO5qcI7fkEES34IVEr+2Uff
JkO2PfyyAYEO/5dBlPh1Undu9WQl6J7B
-----END PUBLIC KEY-----`)
	}

	lv, e := NewLicenseVerifier(pemBytes)
	if e != nil {
		return e
	}

	licInfo, e := lv.Verify(lic, options...)
	if e != nil {
		return e
	}

	if licInfo.DeploymentID != depID {
		return errors.New("Invalid license - deployment ID doesn't match")
	}

	return nil
}
