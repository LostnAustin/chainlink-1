package web_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	p2ppeer "github.com/libp2p/go-libp2p-core/peer"
	"github.com/pelletier/go-toml"
	"github.com/smartcontractkit/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/guregu/null.v4"

	"github.com/smartcontractkit/chainlink/core/internal/cltest"
	"github.com/smartcontractkit/chainlink/core/internal/testutils"
	"github.com/smartcontractkit/chainlink/core/services/directrequest"
	"github.com/smartcontractkit/chainlink/core/services/job"
	"github.com/smartcontractkit/chainlink/core/services/keeper"
	"github.com/smartcontractkit/chainlink/core/services/keystore/keys/ethkey"
	"github.com/smartcontractkit/chainlink/core/services/keystore/keys/p2pkey"
	"github.com/smartcontractkit/chainlink/core/services/pg"
	"github.com/smartcontractkit/chainlink/core/testdata/testspecs"
	"github.com/smartcontractkit/chainlink/core/web"
	"github.com/smartcontractkit/chainlink/core/web/presenters"
)

func TestJobsController_Create_ValidationFailure_OffchainReportingSpec(t *testing.T) {
	var (
		contractAddress = cltest.NewEIP55Address()
	)

	peerID, err := p2ppeer.Decode("12D3KooWPjceQrSwdWXPyLLeABRXmuqt69Rg3sBYbU1Nft9HyQ6X")
	require.NoError(t, err)
	randomBytes := testutils.Random32Byte()

	var tt = []struct {
		name        string
		pid         p2pkey.PeerID
		kb          string
		taExists    bool
		expectedErr error
	}{
		{
			name:        "invalid keybundle",
			pid:         p2pkey.PeerID(peerID),
			kb:          hex.EncodeToString(randomBytes[:]),
			taExists:    true,
			expectedErr: job.ErrNoSuchKeyBundle,
		},
		{
			name:        "invalid transmitter address",
			pid:         p2pkey.PeerID(peerID),
			kb:          cltest.DefaultOCRKeyBundleID,
			taExists:    false,
			expectedErr: job.ErrNoSuchTransmitterKey,
		},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			ta, client := setupJobsControllerTests(t)

			var address ethkey.EIP55Address
			if tc.taExists {
				key, _ := cltest.MustInsertRandomKey(t, ta.KeyStore.Eth())
				address = key.Address
			} else {
				address = cltest.NewEIP55Address()
			}

			ta.KeyStore.OCR().Add(cltest.DefaultOCRKey)

			sp := cltest.MinimalOCRNonBootstrapSpec(contractAddress, address, tc.pid, tc.kb)
			body, _ := json.Marshal(web.CreateJobRequest{
				TOML: sp,
			})
			resp, cleanup := client.Post("/v2/jobs", bytes.NewReader(body))
			t.Cleanup(cleanup)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			b, err := ioutil.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Contains(t, string(b), tc.expectedErr.Error())
		})
	}
}

func TestJobController_Create_DirectRequest_Fast(t *testing.T) {
	app, client := setupJobsControllerTests(t)
	app.KeyStore.OCR().Add(cltest.DefaultOCRKey)
	app.KeyStore.P2P().Add(cltest.DefaultP2PKey)

	n := 10

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			body, err := json.Marshal(web.CreateJobRequest{
				TOML: fmt.Sprintf(testspecs.DirectRequestSpecNoExternalJobID, i),
			})
			require.NoError(t, err)

			t.Logf("POSTing %d", i)
			r, cleanup := client.Post("/v2/jobs", bytes.NewReader(body))
			defer cleanup()
			require.Equal(t, http.StatusOK, r.StatusCode)
		}(i)
	}
	wg.Wait()
	cltest.AssertCount(t, app.GetSqlxDB(), "direct_request_specs", int64(n))
}

func mustInt32FromString(t *testing.T, s string) int32 {
	i, err := strconv.ParseInt(s, 10, 32)
	require.NoError(t, err)
	return int32(i)
}

func TestJobController_Create_HappyPath(t *testing.T) {
	app, client := setupJobsControllerTests(t)
	b1, b2 := setupBridges(t, app.GetSqlxDB(), app.GetConfig())
	app.KeyStore.OCR().Add(cltest.DefaultOCRKey)
	require.NoError(t, app.KeyStore.P2P().Add(cltest.DefaultP2PKey))
	pks, err := app.KeyStore.VRF().GetAll()
	require.NoError(t, err)
	require.Len(t, pks, 1)
	k, err := app.KeyStore.P2P().GetAll()
	require.NoError(t, err)
	require.Len(t, k, 1)

	jorm := app.JobORM()
	var tt = []struct {
		name      string
		toml      string
		assertion func(t *testing.T, r *http.Response)
	}{
		{
			name: "offchain reporting",
			toml: testspecs.GenerateOCRSpec(testspecs.OCRSpecParams{
				TransmitterAddress: app.Key.Address.Hex(),
				DS1BridgeName:      b1,
				DS2BridgeName:      b2,
			}).Toml(),
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)

				resource := presenters.JobResource{}
				err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, r), &resource)
				assert.NoError(t, err)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, resource.OffChainReportingSpec)

				assert.Equal(t, "web oracle spec", jb.Name.ValueOrZero())
				assert.Equal(t, jb.OCROracleSpec.P2PBootstrapPeers, resource.OffChainReportingSpec.P2PBootstrapPeers)
				assert.Equal(t, jb.OCROracleSpec.IsBootstrapPeer, resource.OffChainReportingSpec.IsBootstrapPeer)
				assert.Equal(t, jb.OCROracleSpec.EncryptedOCRKeyBundleID, resource.OffChainReportingSpec.EncryptedOCRKeyBundleID)
				assert.Equal(t, jb.OCROracleSpec.TransmitterAddress, resource.OffChainReportingSpec.TransmitterAddress)
				assert.Equal(t, jb.OCROracleSpec.ObservationTimeout, resource.OffChainReportingSpec.ObservationTimeout)
				assert.Equal(t, jb.OCROracleSpec.BlockchainTimeout, resource.OffChainReportingSpec.BlockchainTimeout)
				assert.Equal(t, jb.OCROracleSpec.ContractConfigTrackerSubscribeInterval, resource.OffChainReportingSpec.ContractConfigTrackerSubscribeInterval)
				assert.Equal(t, jb.OCROracleSpec.ContractConfigTrackerSubscribeInterval, resource.OffChainReportingSpec.ContractConfigTrackerSubscribeInterval)
				assert.Equal(t, jb.OCROracleSpec.ContractConfigConfirmations, resource.OffChainReportingSpec.ContractConfigConfirmations)
				assert.NotNil(t, resource.PipelineSpec.DotDAGSource)
				// Sanity check to make sure it inserted correctly
				require.Equal(t, ethkey.EIP55Address("0x613a38AC1659769640aaE063C651F48E0250454C"), jb.OCROracleSpec.ContractAddress)
			},
		},
		{
			name: "keeper",
			toml: testspecs.GenerateKeeperSpec(testspecs.KeeperSpecParams{
				Name:              "example keeper spec",
				ContractAddress:   "0x9E40733cC9df84636505f4e6Db28DCa0dC5D1bba",
				FromAddress:       "0xa8037A20989AFcBC51798de9762b351D63ff462e",
				ObservationSource: keeper.ExpectedObservationSource,
			}).Toml(),
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)

				resource := presenters.JobResource{}
				b := cltest.ParseResponseBody(t, r)
				err := web.ParseJSONAPIResponse(b, &resource)
				require.NoError(t, err)
				require.NotNil(t, resource.KeeperSpec)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, jb.KeeperSpec)

				require.Equal(t, resource.KeeperSpec.ContractAddress, jb.KeeperSpec.ContractAddress)
				require.Equal(t, resource.KeeperSpec.FromAddress, jb.KeeperSpec.FromAddress)
				assert.Equal(t, "example keeper spec", jb.Name.ValueOrZero())

				// Sanity check to make sure it inserted correctly
				require.Equal(t, ethkey.EIP55Address("0x9E40733cC9df84636505f4e6Db28DCa0dC5D1bba"), jb.KeeperSpec.ContractAddress)
				require.Equal(t, ethkey.EIP55Address("0xa8037A20989AFcBC51798de9762b351D63ff462e"), jb.KeeperSpec.FromAddress)
			},
		},
		{
			name: "cron",
			toml: testspecs.CronSpec,
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)
				resource := presenters.JobResource{}
				err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, r), &resource)
				assert.NoError(t, err)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, jb.CronSpec)

				assert.NotNil(t, resource.PipelineSpec.DotDAGSource)
				require.Equal(t, "CRON_TZ=UTC * 0 0 1 1 *", jb.CronSpec.CronSchedule)
			},
		},
		{
			name: "cron-dot-separator",
			toml: testspecs.CronSpecDotSep,
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)
				resource := presenters.JobResource{}
				err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, r), &resource)
				assert.NoError(t, err)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, jb.CronSpec)

				assert.NotNil(t, resource.PipelineSpec.DotDAGSource)
				require.Equal(t, "CRON_TZ=UTC * 0 0 1 1 *", jb.CronSpec.CronSchedule)
			},
		},
		{
			name: "directrequest",
			toml: testspecs.DirectRequestSpec,
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)
				resource := presenters.JobResource{}
				err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, r), &resource)
				assert.NoError(t, err)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, jb.DirectRequestSpec)

				assert.Equal(t, "example eth request event spec", jb.Name.ValueOrZero())
				assert.NotNil(t, resource.PipelineSpec.DotDAGSource)
				// Sanity check to make sure it inserted correctly
				require.Equal(t, ethkey.EIP55Address("0x613a38AC1659769640aaE063C651F48E0250454C"), jb.DirectRequestSpec.ContractAddress)
				require.NotZero(t, jb.ExternalJobID[:])
			},
		},
		{
			name: "directrequest-with-requesters-and-min-contract-payment",
			toml: testspecs.DirectRequestSpecWithRequestersAndMinContractPayment,
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)
				resource := presenters.JobResource{}
				err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, r), &resource)
				assert.NoError(t, err)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, jb.DirectRequestSpec)

				assert.Equal(t, "example eth request event spec with requesters and min contract payment", jb.Name.ValueOrZero())
				assert.NotNil(t, resource.PipelineSpec.DotDAGSource)
				assert.NotNil(t, resource.DirectRequestSpec.Requesters)
				assert.Equal(t, "1000000000000000000000", resource.DirectRequestSpec.MinContractPayment.String())
				// Check requesters got saved properly
				require.EqualValues(t, []common.Address{common.HexToAddress("0xAaAA1F8ee20f5565510b84f9353F1E333e753B7a"), common.HexToAddress("0xBbBb70f0E81c6F3430dfDc9fa02fB22bDD818c4E")}, jb.DirectRequestSpec.Requesters)
				require.Equal(t, "1000000000000000000000", jb.DirectRequestSpec.MinContractPayment.String())
				require.NotZero(t, jb.ExternalJobID[:])
			},
		},
		{
			name: "fluxmonitor",
			toml: testspecs.FluxMonitorSpec,
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)
				resource := presenters.JobResource{}
				err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, r), &resource)
				assert.NoError(t, err)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, jb.FluxMonitorSpec)

				assert.Equal(t, "example flux monitor spec", jb.Name.ValueOrZero())
				assert.NotNil(t, resource.PipelineSpec.DotDAGSource)
				assert.Equal(t, ethkey.EIP55Address("0x3cCad4715152693fE3BC4460591e3D3Fbd071b42"), jb.FluxMonitorSpec.ContractAddress)
				assert.Equal(t, time.Second, jb.FluxMonitorSpec.IdleTimerPeriod)
				assert.Equal(t, false, jb.FluxMonitorSpec.IdleTimerDisabled)
				assert.Equal(t, float32(0.5), jb.FluxMonitorSpec.Threshold)
				assert.Equal(t, float32(0), jb.FluxMonitorSpec.AbsoluteThreshold)
			},
		},
		{
			name: "vrf",
			toml: testspecs.GenerateVRFSpec(testspecs.VRFSpecParams{PublicKey: pks[0].PublicKey.String()}).Toml(),
			assertion: func(t *testing.T, r *http.Response) {
				require.Equal(t, http.StatusOK, r.StatusCode)
				resp := cltest.ParseResponseBody(t, r)
				resource := presenters.JobResource{}
				err := web.ParseJSONAPIResponse(resp, &resource)
				require.NoError(t, err)

				jb, err := jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
				require.NoError(t, err)
				require.NotNil(t, jb.VRFSpec)

				assert.NotNil(t, resource.PipelineSpec.DotDAGSource)
				assert.Equal(t, uint32(6), resource.VRFSpec.MinIncomingConfirmations)
				assert.Equal(t, jb.VRFSpec.MinIncomingConfirmations, resource.VRFSpec.MinIncomingConfirmations)
				assert.Equal(t, "0xABA5eDc1a551E55b1A570c0e1f1055e5BE11eca7", resource.VRFSpec.CoordinatorAddress.Hex())
				assert.Equal(t, jb.VRFSpec.CoordinatorAddress.Hex(), resource.VRFSpec.CoordinatorAddress.Hex())
			},
		},
	}
	for _, tc := range tt {
		c := tc
		t.Run(c.name, func(t *testing.T) {
			body, err := json.Marshal(web.CreateJobRequest{
				TOML: c.toml,
			})
			require.NoError(t, err)
			response, cleanup := client.Post("/v2/jobs", bytes.NewReader(body))
			defer cleanup()
			c.assertion(t, response)
		})
	}
}

func TestJobsController_Create_WebhookSpec(t *testing.T) {
	app := cltest.NewApplicationEVMDisabled(t)
	require.NoError(t, app.Start(testutils.Context(t)))

	_, fetchBridge := cltest.MustCreateBridge(t, app.GetSqlxDB(), cltest.BridgeOpts{}, app.GetConfig())
	_, submitBridge := cltest.MustCreateBridge(t, app.GetSqlxDB(), cltest.BridgeOpts{}, app.GetConfig())

	client := app.NewHTTPClient()

	tomlStr := fmt.Sprintf(testspecs.WebhookSpecNoBody, fetchBridge.Name.String(), submitBridge.Name.String())
	body, _ := json.Marshal(web.CreateJobRequest{
		TOML: tomlStr,
	})
	response, cleanup := client.Post("/v2/jobs", bytes.NewReader(body))
	defer cleanup()
	require.Equal(t, http.StatusOK, response.StatusCode)
	resource := presenters.JobResource{}
	err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, response), &resource)
	require.NoError(t, err)
	assert.NotNil(t, resource.PipelineSpec.DotDAGSource)

	jorm := app.JobORM()
	_, err = jorm.FindJob(context.Background(), mustInt32FromString(t, resource.ID))
	require.NoError(t, err)
}

func TestJobsController_FailToCreate_EmptyJsonAttribute(t *testing.T) {
	app := cltest.NewApplicationEVMDisabled(t)
	require.NoError(t, app.Start(testutils.Context(t)))

	client := app.NewHTTPClient()

	tomlBytes := cltest.MustReadFile(t, "../testdata/tomlspecs/webhook-job-spec-with-empty-json.toml")
	body, _ := json.Marshal(web.CreateJobRequest{
		TOML: string(tomlBytes),
	})
	response, cleanup := client.Post("/v2/jobs", bytes.NewReader(body))
	defer cleanup()

	b, err := ioutil.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(b), "syntax is not supported. Please use \\\"{}\\\" instead")
}

func TestJobsController_Index_HappyPath(t *testing.T) {
	_, client, ocrJobSpecFromFile, _, ereJobSpecFromFile, _ := setupJobSpecsControllerTestsWithJobs(t)

	response, cleanup := client.Get("/v2/jobs")
	t.Cleanup(cleanup)
	cltest.AssertServerResponse(t, response, http.StatusOK)

	resources := []presenters.JobResource{}
	err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, response), &resources)
	assert.NoError(t, err)

	require.Len(t, resources, 2)

	runDirectRequestJobSpecAssertions(t, ereJobSpecFromFile, resources[0])
	runOCRJobSpecAssertions(t, ocrJobSpecFromFile, resources[1])
}

func TestJobsController_Show_HappyPath(t *testing.T) {
	_, client, ocrJobSpecFromFile, jobID, ereJobSpecFromFile, jobID2 := setupJobSpecsControllerTestsWithJobs(t)

	response, cleanup := client.Get("/v2/jobs/" + fmt.Sprintf("%v", jobID))
	t.Cleanup(cleanup)
	cltest.AssertServerResponse(t, response, http.StatusOK)

	ocrJob := presenters.JobResource{}
	err := web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, response), &ocrJob)
	assert.NoError(t, err)

	runOCRJobSpecAssertions(t, ocrJobSpecFromFile, ocrJob)

	response, cleanup = client.Get("/v2/jobs/" + ocrJobSpecFromFile.ExternalJobID.String())
	t.Cleanup(cleanup)
	cltest.AssertServerResponse(t, response, http.StatusOK)

	ocrJob = presenters.JobResource{}
	err = web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, response), &ocrJob)
	assert.NoError(t, err)

	runOCRJobSpecAssertions(t, ocrJobSpecFromFile, ocrJob)

	response, cleanup = client.Get("/v2/jobs/" + fmt.Sprintf("%v", jobID2))
	t.Cleanup(cleanup)
	cltest.AssertServerResponse(t, response, http.StatusOK)

	ereJob := presenters.JobResource{}
	err = web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, response), &ereJob)
	assert.NoError(t, err)

	runDirectRequestJobSpecAssertions(t, ereJobSpecFromFile, ereJob)

	response, cleanup = client.Get("/v2/jobs/" + ereJobSpecFromFile.ExternalJobID.String())
	t.Cleanup(cleanup)
	cltest.AssertServerResponse(t, response, http.StatusOK)

	ereJob = presenters.JobResource{}
	err = web.ParseJSONAPIResponse(cltest.ParseResponseBody(t, response), &ereJob)
	assert.NoError(t, err)

	runDirectRequestJobSpecAssertions(t, ereJobSpecFromFile, ereJob)
}

func TestJobsController_Show_InvalidID(t *testing.T) {
	_, client, _, _, _, _ := setupJobSpecsControllerTestsWithJobs(t)

	response, cleanup := client.Get("/v2/jobs/uuidLikeString")
	t.Cleanup(cleanup)
	cltest.AssertServerResponse(t, response, http.StatusUnprocessableEntity)
}

func TestJobsController_Show_NonExistentID(t *testing.T) {
	_, client, _, _, _, _ := setupJobSpecsControllerTestsWithJobs(t)

	response, cleanup := client.Get("/v2/jobs/999999999")
	t.Cleanup(cleanup)

	cltest.AssertServerResponse(t, response, http.StatusNotFound)
}

func runOCRJobSpecAssertions(t *testing.T, ocrJobSpecFromFileDB job.Job, ocrJobSpecFromServer presenters.JobResource) {
	ocrJobSpecFromFile := ocrJobSpecFromFileDB.OCROracleSpec
	assert.Equal(t, ocrJobSpecFromFile.ContractAddress, ocrJobSpecFromServer.OffChainReportingSpec.ContractAddress)
	assert.Equal(t, ocrJobSpecFromFile.P2PBootstrapPeers, ocrJobSpecFromServer.OffChainReportingSpec.P2PBootstrapPeers)
	assert.Equal(t, ocrJobSpecFromFile.IsBootstrapPeer, ocrJobSpecFromServer.OffChainReportingSpec.IsBootstrapPeer)
	assert.Equal(t, ocrJobSpecFromFile.EncryptedOCRKeyBundleID, ocrJobSpecFromServer.OffChainReportingSpec.EncryptedOCRKeyBundleID)
	assert.Equal(t, ocrJobSpecFromFile.TransmitterAddress, ocrJobSpecFromServer.OffChainReportingSpec.TransmitterAddress)
	assert.Equal(t, ocrJobSpecFromFile.ObservationTimeout, ocrJobSpecFromServer.OffChainReportingSpec.ObservationTimeout)
	assert.Equal(t, ocrJobSpecFromFile.BlockchainTimeout, ocrJobSpecFromServer.OffChainReportingSpec.BlockchainTimeout)
	assert.Equal(t, ocrJobSpecFromFile.ContractConfigTrackerSubscribeInterval, ocrJobSpecFromServer.OffChainReportingSpec.ContractConfigTrackerSubscribeInterval)
	assert.Equal(t, ocrJobSpecFromFile.ContractConfigTrackerSubscribeInterval, ocrJobSpecFromServer.OffChainReportingSpec.ContractConfigTrackerSubscribeInterval)
	assert.Equal(t, ocrJobSpecFromFile.ContractConfigConfirmations, ocrJobSpecFromServer.OffChainReportingSpec.ContractConfigConfirmations)
	assert.Equal(t, ocrJobSpecFromFileDB.Pipeline.Source, ocrJobSpecFromServer.PipelineSpec.DotDAGSource)

	// Check that create and update dates are non empty values.
	// Empty date value is "0001-01-01 00:00:00 +0000 UTC" so we are checking for the
	// millenia and century characters to be present
	assert.Contains(t, ocrJobSpecFromServer.OffChainReportingSpec.CreatedAt.String(), "20")
	assert.Contains(t, ocrJobSpecFromServer.OffChainReportingSpec.UpdatedAt.String(), "20")
}

func runDirectRequestJobSpecAssertions(t *testing.T, ereJobSpecFromFile job.Job, ereJobSpecFromServer presenters.JobResource) {
	assert.Equal(t, ereJobSpecFromFile.DirectRequestSpec.ContractAddress, ereJobSpecFromServer.DirectRequestSpec.ContractAddress)
	assert.Equal(t, ereJobSpecFromFile.Pipeline.Source, ereJobSpecFromServer.PipelineSpec.DotDAGSource)
	// Check that create and update dates are non empty values.
	// Empty date value is "0001-01-01 00:00:00 +0000 UTC" so we are checking for the
	// millenia and century characters to be present
	assert.Contains(t, ereJobSpecFromServer.DirectRequestSpec.CreatedAt.String(), "20")
	assert.Contains(t, ereJobSpecFromServer.DirectRequestSpec.UpdatedAt.String(), "20")
}

func setupBridges(t *testing.T, db *sqlx.DB, cfg pg.LogConfig) (b1, b2 string) {
	_, bridge := cltest.MustCreateBridge(t, db, cltest.BridgeOpts{}, cfg)
	_, bridge2 := cltest.MustCreateBridge(t, db, cltest.BridgeOpts{}, cfg)
	return bridge.Name.String(), bridge2.Name.String()
}

func setupJobsControllerTests(t *testing.T) (ta *cltest.TestApplication, cc cltest.HTTPClientCleaner) {
	cfg := cltest.NewTestGeneralConfig(t)
	cfg.Overrides.FeatureOffchainReporting = null.BoolFrom(true)
	app := cltest.NewApplicationWithConfigAndKey(t, cfg)
	require.NoError(t, app.Start(testutils.Context(t)))

	client := app.NewHTTPClient()
	vrfKeyStore := app.GetKeyStore().VRF()
	_, err := vrfKeyStore.Create()
	require.NoError(t, err)
	return app, client
}

func setupJobSpecsControllerTestsWithJobs(t *testing.T) (*cltest.TestApplication, cltest.HTTPClientCleaner, job.Job, int32, job.Job, int32) {
	cfg := cltest.NewTestGeneralConfig(t)
	cfg.Overrides.FeatureOffchainReporting = null.BoolFrom(true)
	app := cltest.NewApplicationWithConfigAndKey(t, cfg)

	require.NoError(t, app.KeyStore.OCR().Add(cltest.DefaultOCRKey))
	require.NoError(t, app.Start(testutils.Context(t)))

	_, bridge := cltest.MustCreateBridge(t, app.GetSqlxDB(), cltest.BridgeOpts{}, app.GetConfig())
	_, bridge2 := cltest.MustCreateBridge(t, app.GetSqlxDB(), cltest.BridgeOpts{}, app.GetConfig())

	client := app.NewHTTPClient()

	var jb job.Job
	ocrspec := testspecs.GenerateOCRSpec(testspecs.OCRSpecParams{DS1BridgeName: bridge.Name.String(), DS2BridgeName: bridge2.Name.String()})
	err := toml.Unmarshal([]byte(ocrspec.Toml()), &jb)
	require.NoError(t, err)
	var ocrSpec job.OCROracleSpec
	err = toml.Unmarshal([]byte(ocrspec.Toml()), &ocrspec)
	require.NoError(t, err)
	jb.OCROracleSpec = &ocrSpec
	jb.OCROracleSpec.TransmitterAddress = &app.Key.Address
	err = app.AddJobV2(context.Background(), &jb)
	require.NoError(t, err)

	erejb, err := directrequest.ValidatedDirectRequestSpec(string(cltest.MustReadFile(t, "../testdata/tomlspecs/direct-request-spec.toml")))
	require.NoError(t, err)
	err = app.AddJobV2(context.Background(), &erejb)
	require.NoError(t, err)

	return app, client, jb, jb.ID, erejb, erejb.ID
}
