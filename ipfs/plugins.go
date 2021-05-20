package ipfs

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/ipfs/go-ipfs/plugin/loader"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
)

// pluginsOnce ensures that plugins are loaded/injected only once in a runtime.
// Otherwise repeatable loading of preloaded IPFS plugins errors.
// This is specifically needed for tests in 'node' package which spin up multiple embedded nodes.
// TODO(liamsi): consider using Mock() in those tests.
var pluginsOnce sync.Once

// one and only loader instance within the application
var ldr *loader.PluginLoader

// plugins loads default and custom IPFS plugins.
// Even though the NMT plugin can be loaded in much simpler way(plugin.EnableNMT),
// we still need to ensure default plugins likes "badger" are loaded.
func plugins(path string) (err error) {
	pluginsOnce.Do(func() {
		ldr, err = loader.NewPluginLoader(filepath.Join(path, "plugins"))
		if err != nil {
			err = fmt.Errorf("error loading plugins: %w", err)
			return
		}

		if err = ldr.Load(&plugin.Nmt{}); err != nil {
			err = fmt.Errorf("error loading NMT plugin: %w", err)
			return
		}

		if err = ldr.Initialize(); err != nil {
			err = fmt.Errorf("error initializing plugins:%w", err)
			return
		}

		if err = ldr.Inject(); err != nil {
			err = fmt.Errorf("error injecting plugins: %w", err)
			return
		}
	})

	// TODO: Ideally we should handle ldr.Start()/ldr.Close() as well, however IPFS plugins system struggles from side
	//  effects. Those methods force us to create PluginLoader for every IPFS Node/Repo, but we
	//  can't create multiple PluginLoader instances in a runtime as each preloads default IPFS plugins producing errors
	//  with no possible workaround due to strong encapsulation.
	return
}
