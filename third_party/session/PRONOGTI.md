# Pronogti session source snapshot

This directory retains `github.com/dronm/session` revision
`444cd0bd6dad` with its original license and public interfaces.

The Redis implementation has one compatibility fix: each session manager gets
its own provider instance, and every Redis session store retains that provider.
Creating or closing the administrator manager therefore cannot change the
customer manager's Redis namespace, connection or expiration settings.

The optional `NewProvider() Provider` factory is detected by `NewManager`.
Existing providers that do not implement it keep their original registration
behavior. `Session`, `Provider`, `Register` and `NewManager` signatures are
unchanged. The application uses the source snapshot through a relative module
replacement.

Run the isolated provider regression against an explicitly configured local
Redis server:

```sh
SESSION_TEST_REDIS_URL=redis://127.0.0.1:6379/0 \
	go test -race -run TestManagersKeepRedisProvidersAndStoresIsolated ./redis
```

The test creates and removes only randomly named test namespaces. It verifies
independent values, typed reads, lifetimes, concurrent access, deletion,
namespace cleanup, reconnection and connection closure. The older provider
tests have additional environment/database prerequisites and are not included
by this command.
