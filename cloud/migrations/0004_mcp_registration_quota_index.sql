-- Limit the rolling registration quota query to recently created dynamic clients.
CREATE INDEX mcp_clients_dynamic_created ON mcp_clients(created_at) WHERE user_id IS NULL;
