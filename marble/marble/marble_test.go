package marble

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/edgelesssys/coordinator/coordinator/core"
	"github.com/edgelesssys/coordinator/coordinator/quote"
	"github.com/edgelesssys/coordinator/coordinator/server"

	"github.com/stretchr/testify/assert"
)

const manifestJSON string = `{
	"Packages": {
		"backend": {
			"UniqueID": [0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31],
			"Debug": false
		},
		"frontend": {
			"SignerID": [31,30,29,28,27,26,25,24,23,22,21,20,19,18,17,16,15,14,13,12,11,10,9,8,7,6,5,4,3,2,1,0],
			"ProductID": [44],
			"SecurityVersion": 3,
			"Debug": true
		}
	},
	"Infrastructures": {
		"Azure": {
			"QESVN": 2,
			"PCESVN": 3,
			"CPUSVN": [0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15],
			"RootCA": [3,3,3]
		},
		"Alibaba": {
			"QESVN": 2,
			"PCESVN": 4,
			"CPUSVN": [15,14,13,12,11,10,9,8,7,6,5,4,3,2,1,0],
			"RootCA": [4,4,4]
		}
	},
	"Marbles": {
		"backend_first": {
			"Package": "backend",
			"MaxActivations": 1,
			"Parameters": {
				"Files": {
					"/abc/defg.txt": [7,7,7],
					"/ghi/jkl.mno": [8,8,8]
				},
				"Env": {
					"IS_FIRST": "true"
				},
				"Argv": [
					"--first",
					"serve"
				]
			}
		},
		"backend_other": {
			"Package": "backend",
			"Parameters": {
				"Argv": [
					"serve"
				]
			}
		},
		"frontend": {
			"Package": "frontend"
		}
	},
	"Clients": {
		"owner": [9,9,9]
	}
}`

const coordinatorCommonName string = "Coordinator" // TODO: core does not export this, for now just use it hardcoded

func TestLogic(t *testing.T) {
	assert := assert.New(t)

	// parse manifest
	var manifest core.Manifest
	err := json.Unmarshal([]byte(manifestJSON), &manifest)
	assert.Nil(err, err)
	validator := quote.NewMockValidator()
	issuer := quote.NewMockIssuer()

	// create core and run gRPC server
	coordinator, err := core.NewCore(orgName, validator, issuer)
	assert.NotNil(coordinator, "coordinator empty")
	assert.Nil(err, err)

	coordinator.SetManifest(context.TODO(), []byte(manifestJSON))

	// run marble server
	var grpcAddr string
	addrChan := make(chan string)
	errChan := make(chan error)
	marbleServerAddr := flag.String("ip", "localhost:0", "")
	go server.RunMarbleServer(coordinator, *marbleServerAddr, addrChan, errChan)
	select {
	case err = <-errChan:
		fmt.Println("Failed to start gRPC server", err)
	case grpcAddr = <-addrChan:
		fmt.Println("start mesh server at", grpcAddr)
	}

	spawner := marbleSpawner{
		assert:     assert,
		issuer:     issuer,
		validator:  validator,
		manifest:   manifest,
		serverAddr: grpcAddr,
	}
	// activate first backend
	spawner.newMarble("backend_first", "Azure", true)

	// try to activate another first backend
	spawner.newMarble("backend_first", "Azure", false)

	// activate 10 other backend
	pickInfra := func(i int) string {
		if i&1 == 0 {
			return "Azure"
		}
		return "Alibaba"
	}
	for i := 0; i < 10; i++ {
		spawner.newMarble("backend_other", pickInfra(i), true)
	}

	// activate 10 frontend
	for i := 0; i < 10; i++ {
		spawner.newMarble("frontend", pickInfra(i), true)
	}

}

type marbleSpawner struct {
	manifest   core.Manifest
	validator  *quote.MockValidator
	issuer     quote.Issuer
	serverAddr string
	assert     *assert.Assertions
}

func (ms marbleSpawner) newMarble(marbleType string, infraName string, shouldSucceed bool) {
	// set env vars
	err := os.Setenv(EdgCoordinatorAddr, ms.serverAddr)
	ms.assert.Nil(err, "failed to set env variable: %v", err)
	err = os.Setenv(EdgMarbleType, marbleType)
	ms.assert.Nil(err, "failed to set env variable: %v", err)

	// create Authenticator
	commonName := "marble"          // Coordinator will assign an ID to us
	issuer := quote.NewMockIssuer() // TODO: Use real issuer
	a, err := NewAuthenticator(orgName, commonName, issuer)
	ms.assert.Nil(err, "failed to create Authenticator: %v", err)
	ms.assert.NotNil(a, "got empty Authenticator")

	// store quote in validator
	marble, ok := ms.manifest.Marbles[marbleType]
	ms.assert.True(ok, "marbleType '%v' does not exist", marbleType)
	pkg, ok := ms.manifest.Packages[marble.Package]
	ms.assert.True(ok, "Package '%v' does not exist", marble.Package)
	infra, ok := ms.manifest.Infrastructures[infraName]
	ms.assert.True(ok, "Infrastructure '%v' does not exist", infraName)
	ms.validator.AddValidQuote(a.quote, a.initCert.Raw, pkg, infra)

	// call preMain
	cert, params, err := PreMain(a)
	if !shouldSucceed {
		ms.assert.NotNil(err, err)
		ms.assert.Nil(cert, "expected empty cert, but got %v", cert)
		ms.assert.Nil(params, "expected empty params, but got %v", params)
		return
	}
	ms.assert.Nil(err, "preMain failed: %v", err)
	ms.assert.NotNil(cert, "got empty cert: %v", cert)
	ms.assert.NotNil(params, "got empty params: %v", params)

	ms.assert.Equal(marble.Parameters.Files, a.params.Files, "expected equal: '%v' - '%v'", marble.Parameters.Files, a.params.Files)
	ms.assert.Equal(marble.Parameters.Env, a.params.Env, "expected equal: '%v' - '%v'", marble.Parameters.Env, a.params.Env)
	ms.assert.Equal(marble.Parameters.Argv, a.params.Argv, "expected equal: '%v' - '%v'", marble.Parameters.Argv, a.params.Argv)

	ms.assert.Equal(a.marbleCert.Issuer.CommonName, coordinatorCommonName, "expected equal: '%v' - '%v'", a.marbleCert.Issuer.CommonName, coordinatorCommonName)
	ms.assert.Equal(a.marbleCert.Issuer.Organization[0], orgName, "expected equal: '%v' - '%v'", a.marbleCert.Issuer.Organization[0], orgName)
	// commonName gets overwritten
	// assert.Equal(a.marbleCert.Subject.CommonName, a.commonName)
	ms.assert.Equal(a.marbleCert.Subject.Organization[0], a.orgName, "expected equal: '%v' - '%v'", a.marbleCert.Subject.Organization[0], a.orgName)
	ms.assert.Equal(a.marbleCert.PublicKey, a.pubk, "expected equal: '%v' - '%v'", a.marbleCert.PublicKey, a.pubk)
}