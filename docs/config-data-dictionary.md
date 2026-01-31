This page documents the configuration keys for electrum-go.

|Section|Key|Type|Description|
|---|---|---|---|
|server|listen|string|Address and port to bind for Electrum client connections|
|server|max_connections|int|Maximum number of concurrent client connections|
|server|request_timeout_seconds|int|Seconds of inactivity before disconnecting a client|
|bitcoin|rpc_host|string|Bitcoin Core RPC endpoint in host:port format|
|bitcoin|rpc_user|string|Bitcoin Core RPC username|
|bitcoin|rpc_pass|string|Bitcoin Core RPC password|
|bitcoin|zmq_block_addr|string|ZMQ endpoint for raw block notifications|
|bitcoin|zmq_tx_addr|string|ZMQ endpoint for raw transaction notifications|
|storage|db_path|string|Path to the Pebble database directory|
|storage|max_reorg_depth|int|Maximum reorg depth to keep undo logs for|
|indexer|start_height|int|Height to begin indexing from; -1 for tip|
|indexer|checkpoint_interval|int|Blocks between checkpoint saves during initial sync|
|indexer|undo_prune_interval|int|Blocks between undo log pruning runs|
|logging|level|string|Logging level: debug, info, warn, error|
|logging|log_requests|bool|Whether to log each Electrum RPC request|