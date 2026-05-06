package main

import "github.com/udisondev/udisend/internal/config"

// Profile and DeployMode were moved to internal/config; the file-local
// aliases keep the rest of cmd/messenger compact while the package-
// boundary refactor is in flight.
type (
	profile    = config.Profile
	deployMode = config.DeployMode
)

const (
	modeLoopback        = config.ModeLoopback
	modeLANIP           = config.ModeLANIP
	modePublicAutocert  = config.ModePublicAutocert
	modePublicProxy     = config.ModePublicProxy
	modePublicTailscale = config.ModePublicTailscale

	loopbackHost    = config.LoopbackHost
	defaultHTTPPort = config.DefaultHTTPPort
	defaultP2PPort  = config.DefaultP2PPort
)

const (
	settingDeployMode       = config.SettingDeployMode
	settingDeployBindHTTP   = config.SettingDeployBindHTTP
	settingDeployBindP2P    = config.SettingDeployBindP2P
	settingDeployPublicHost = config.SettingDeployPublicHost
	settingDeployTLSCert    = config.SettingDeployTLSCert
	settingDeployTLSKey     = config.SettingDeployTLSKey
	settingDeployTrustProxy = config.SettingDeployTrustProxy
)

var (
	loadProfile       = config.LoadProfile
	saveProfile       = config.SaveProfile
	defaultStorageDir = config.DefaultStorageDir
	isLoopbackBind    = config.IsLoopbackBind
)
