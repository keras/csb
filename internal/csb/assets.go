package csb

import "io/fs"

// Assets holds the resources embedded into the binary at build time.
type Assets struct {
	Entrypoint []byte // entrypoint.sh
	Persist    []byte // csb-persist.sh
	Help       []byte // csb-help.sh
	HostRun    []byte // csb-host-run.tar.xz
	AddonsFS   fs.FS  // embedded addons tree
}
