package app

import (
	_ "embed"
	"github.com/allora-network/allora-chain/x/emissions"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	icatypes "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
	"io"
	"os"
	"path/filepath"

	dbm "github.com/cosmos/cosmos-db"

	"cosmossdk.io/core/appconfig"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"

	storetypes "cosmossdk.io/store/types"
	upgradekeeper "cosmossdk.io/x/upgrade/keeper"
	emissionsKeeper "github.com/allora-network/allora-chain/x/emissions/keeper"
	mintkeeper "github.com/allora-network/allora-chain/x/mint/keeper"
	minttypes "github.com/allora-network/allora-chain/x/mint/types"
	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	consensuskeeper "github.com/cosmos/cosmos-sdk/x/consensus/keeper"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	distrtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	"github.com/cosmos/cosmos-sdk/x/genutil"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	paramskeeper "github.com/cosmos/cosmos-sdk/x/params/keeper"
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	capabilitykeeper "github.com/cosmos/ibc-go/modules/capability/keeper"
	capabilitytypes "github.com/cosmos/ibc-go/modules/capability/types"
	icacontrollerkeeper "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/controller/keeper"
	icahostkeeper "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/host/keeper"
	ibcfeekeeper "github.com/cosmos/ibc-go/v8/modules/apps/29-fee/keeper"
	ibcfeetypes "github.com/cosmos/ibc-go/v8/modules/apps/29-fee/types"
	ibctransferkeeper "github.com/cosmos/ibc-go/v8/modules/apps/transfer/keeper"
	ibctransfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	ibckeeper "github.com/cosmos/ibc-go/v8/modules/core/keeper"
	ibctestingtypes "github.com/cosmos/ibc-go/v8/testing/types"

	_ "cosmossdk.io/api/cosmos/tx/config/v1" // import for side-effects
	_ "cosmossdk.io/x/upgrade"
	_ "github.com/allora-network/allora-chain/x/emissions/module"
	_ "github.com/allora-network/allora-chain/x/mint/module" // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/auth"                  // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/auth/tx/config"        // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/bank"                  // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/consensus"             // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/distribution"          // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/params"                // import for side-effects
	_ "github.com/cosmos/cosmos-sdk/x/staking"               // import for side-effects
)

// DefaultNodeHome default home directories for the application daemon
var DefaultNodeHome string

//go:embed app.yaml
var AppConfigYAML []byte

var (
	_ runtime.AppI            = (*AlloraApp)(nil)
	_ servertypes.Application = (*AlloraApp)(nil)
)

// AlloraApp extends an ABCI application, but with most of its parameters exported.
// They are exported for convenience in creating helper functions, as object
// capabilities aren't needed for testing.
type AlloraApp struct {
	*runtime.App
	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Codec
	txConfig          client.TxConfig
	interfaceRegistry codectypes.InterfaceRegistry

	// keepers
	AccountKeeper         authkeeper.AccountKeeper
	BankKeeper            bankkeeper.Keeper
	StakingKeeper         *stakingkeeper.Keeper
	DistrKeeper           distrkeeper.Keeper
	ConsensusParamsKeeper consensuskeeper.Keeper
	MintKeeper            mintkeeper.Keeper
	EmissionsKeeper       emissionsKeeper.Keeper
	ParamsKeeper          paramskeeper.Keeper
	UpgradeKeeper         *upgradekeeper.Keeper

	// IBC
	IBCKeeper           *ibckeeper.Keeper // IBC Keeper must be a pointer in the app, so we can SetRouter on it correctly
	CapabilityKeeper    *capabilitykeeper.Keeper
	IBCFeeKeeper        ibcfeekeeper.Keeper
	ICAControllerKeeper icacontrollerkeeper.Keeper
	ICAHostKeeper       icahostkeeper.Keeper
	TransferKeeper      ibctransferkeeper.Keeper

	// Scoped IBC
	ScopedIBCKeeper           capabilitykeeper.ScopedKeeper
	ScopedIBCTransferKeeper   capabilitykeeper.ScopedKeeper
	ScopedICAControllerKeeper capabilitykeeper.ScopedKeeper
	ScopedICAHostKeeper       capabilitykeeper.ScopedKeeper

	// simulation manager
	sm *module.SimulationManager
}

func init() {
	userHomeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}

	DefaultNodeHome = filepath.Join(userHomeDir, ".allorad")
}

// AppConfig returns the default app config.
func AppConfig() depinject.Config {
	return depinject.Configs(
		appconfig.LoadYAML(AppConfigYAML),
		depinject.Supply(
			// supply custom module basics
			map[string]module.AppModuleBasic{
				genutiltypes.ModuleName: genutil.NewAppModuleBasic(genutiltypes.DefaultMessageValidator),
			},
		),
	)
}

// NewAlloraApp returns a reference to an initialized AlloraApp.
func NewAlloraApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) (*AlloraApp, error) {
	var (
		app        = &AlloraApp{}
		appBuilder *runtime.AppBuilder
	)

	if err := depinject.Inject(
		depinject.Configs(
			AppConfig(),
			depinject.Supply(
				logger,
				appOpts,
			),
		),
		&appBuilder,
		&app.appCodec,
		&app.legacyAmino,
		&app.txConfig,
		&app.interfaceRegistry,
		&app.AccountKeeper,
		&app.BankKeeper,
		&app.StakingKeeper,
		&app.DistrKeeper,
		&app.ConsensusParamsKeeper,
		&app.MintKeeper,
		&app.EmissionsKeeper,
		&app.UpgradeKeeper,
		&app.ParamsKeeper,
	); err != nil {
		return nil, err
	}

	app.App = appBuilder.Build(db, traceStore, baseAppOptions...)

	// Register legacy modules
	app.registerIBCModules()

	// register streaming services
	if err := app.RegisterStreamingServices(appOpts, app.kvStoreKeys()); err != nil {
		return nil, err
	}

	/****  Module Options ****/

	//begin_blockers: [capability, emissions, distribution, staking, mint, ibc, transfer, genutil, interchainaccounts, feeibc]
	//end_blockers: [staking, ibc, transfer, capability, genutil, interchainaccounts, feeibc, emissions]
	app.ModuleManager.SetOrderBeginBlockers(
		capabilitytypes.ModuleName,
		emissions.ModuleName,
		distrtypes.ModuleName,
		stakingtypes.ModuleName,
		minttypes.ModuleName,
		ibcexported.ModuleName,
		ibctransfertypes.ModuleName,
		genutiltypes.ModuleName,
		icatypes.ModuleName,
		ibcfeetypes.ModuleName,
	)
	app.ModuleManager.SetOrderEndBlockers(
		stakingtypes.ModuleName,
		ibcexported.ModuleName,
		ibctransfertypes.ModuleName,
		capabilitytypes.ModuleName,
		genutiltypes.ModuleName,
		icatypes.ModuleName,
		ibcfeetypes.ModuleName,
		emissions.ModuleName,
	)

	// create the simulation manager and define the order of the modules for deterministic simulations
	// NOTE: this is not required apps that don't use the simulator for fuzz testing transactions
	app.sm = module.NewSimulationManagerFromAppModules(app.ModuleManager.Modules, make(map[string]module.AppModuleSimulation, 0))
	app.sm.RegisterStoreDecoders()

	topicsHandler := NewTopicsHandler(app.EmissionsKeeper)
	app.SetPrepareProposal(topicsHandler.PrepareProposalHandler())

	app.SetInitChainer(app.InitChainer)

	if err := app.Load(loadLatest); err != nil {
		return nil, err
	}

	return app, nil
}

// LegacyAmino returns AlloraApp's amino codec.
func (app *AlloraApp) LegacyAmino() *codec.LegacyAmino {
	return app.legacyAmino
}

// AppCodec returns App's app codec.
//
// NOTE: This is solely to be used for testing purposes as it may be desirable
// for modules to register their own custom testing types.
func (app *AlloraApp) AppCodec() codec.Codec {
	return app.appCodec
}

// GetKey returns the KVStoreKey for the provided store key.
func (app *AlloraApp) GetKey(storeKey string) *storetypes.KVStoreKey {
	sk := app.UnsafeFindStoreKey(storeKey)
	kvStoreKey, ok := sk.(*storetypes.KVStoreKey)
	if !ok {
		return nil
	}
	return kvStoreKey
}

// GetMemKey returns the MemoryStoreKey for the provided store key.
func (app *AlloraApp) GetMemKey(storeKey string) *storetypes.MemoryStoreKey {
	key, ok := app.UnsafeFindStoreKey(storeKey).(*storetypes.MemoryStoreKey)
	if !ok {
		return nil
	}

	return key
}

func (app *AlloraApp) kvStoreKeys() map[string]*storetypes.KVStoreKey {
	keys := make(map[string]*storetypes.KVStoreKey)
	for _, k := range app.GetStoreKeys() {
		if kv, ok := k.(*storetypes.KVStoreKey); ok {
			keys[kv.Name()] = kv
		}
	}

	return keys
}

// GetSubspace returns a param subspace for a given module name.
func (app *AlloraApp) GetSubspace(moduleName string) paramstypes.Subspace {
	subspace, _ := app.ParamsKeeper.GetSubspace(moduleName)
	return subspace
}

// GetIBCKeeper returns the IBC keeper.
func (app *AlloraApp) GetIBCKeeper() *ibckeeper.Keeper {
	return app.IBCKeeper
}

// GetCapabilityScopedKeeper returns the capability scoped keeper.
func (app *AlloraApp) GetCapabilityScopedKeeper(moduleName string) capabilitykeeper.ScopedKeeper {
	return app.CapabilityKeeper.ScopeToModule(moduleName)
}

// SimulationManager implements the SimulationApp interface
func (app *AlloraApp) SimulationManager() *module.SimulationManager {
	return app.sm
}

// RegisterAPIRoutes registers all application module routes with the provided
// API server.
func (app *AlloraApp) RegisterAPIRoutes(apiSvr *api.Server, apiConfig config.APIConfig) {
	app.App.RegisterAPIRoutes(apiSvr, apiConfig)
	// register swagger API in app.go so that other applications can override easily
	if err := server.RegisterSwaggerAPI(apiSvr.ClientCtx, apiSvr.Router, apiConfig.Swagger); err != nil {
		panic(err)
	}
}

// ibctesting.TestingApp compatibility
func (app *AlloraApp) GetBaseApp() *baseapp.BaseApp {
	return app.App.BaseApp
}

// ibctesting.TestingApp compatibility
func (app *AlloraApp) GetStakingKeeper() ibctestingtypes.StakingKeeper {
	return app.StakingKeeper
}

// ibctesting.TestingApp compatibility
func (app *AlloraApp) GetScopedIBCKeeper() capabilitykeeper.ScopedKeeper {
	return app.ScopedIBCKeeper
}

// ibctesting.TestingApp compatibility
func (app *AlloraApp) GetTxConfig() client.TxConfig {
	return app.txConfig
}

// ibctesting.TestingApp compatibility
func (app *AlloraApp) LastCommitID() storetypes.CommitID {
	return app.BaseApp.LastCommitID()
}

// ibctesting.TestingApp compatibility
func (app *AlloraApp) LastBlockHeight() int64 {
	return app.BaseApp.LastBlockHeight()
}
