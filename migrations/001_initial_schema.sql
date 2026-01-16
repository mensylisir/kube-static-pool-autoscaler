-- +goose Up
-- +goose StatementBegin

-- Create enum types
DO $$ BEGIN
    CREATE TYPE machine_state AS ENUM (
        'available',
        'joining',
        'in_use',
        'draining',
        'leaving',
        'maintenance',
        'fault',
        'archived'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

DO $$ BEGIN
    CREATE TYPE nodepool_state AS ENUM (
        'active',
        'disabled',
        'deleting'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

DO $$ BEGIN
    CREATE TYPE discovery_status AS ENUM (
        'pending',
        'success',
        'failed',
        'timeout',
        'skipped'
    );
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- Create machines table
CREATE TABLE IF NOT EXISTS machines (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    ip_address INET NOT NULL,
    ssh_user VARCHAR(255) NOT NULL,
    ssh_port INTEGER NOT NULL DEFAULT 22,
    ssh_key_path TEXT,
    state machine_state NOT NULL DEFAULT 'available',
    nodepool_id UUID,
    labels JSONB DEFAULT '{}',
    taints JSONB DEFAULT '[]',
    cpu INTEGER,
    memory BIGINT,
    gpu VARCHAR(255),
    gpu_count INTEGER,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    maintenance_mode BOOLEAN NOT NULL DEFAULT FALSE,
    fault_reason TEXT,

    CONSTRAINT fk_nodepool
        FOREIGN KEY(nodepool_id)
        REFERENCES nodepools(id)
        ON DELETE SET NULL
);

-- Create nodepools table
CREATE TABLE IF NOT EXISTS nodepools (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL UNIQUE,
    min_size INTEGER NOT NULL DEFAULT 0,
    max_size INTEGER NOT NULL DEFAULT 10,
    state nodepool_state NOT NULL DEFAULT 'active',
    labels JSONB DEFAULT '{}',
    taints JSONB DEFAULT '[]',
    cpu INTEGER,
    memory BIGINT,
    gpu VARCHAR(255),
    gpu_count INTEGER,
    instance_type VARCHAR(255),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- Create discovery results table
CREATE TABLE IF NOT EXISTS discovery_results (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ip_address INET NOT NULL,
    ssh_user VARCHAR(255) NOT NULL,
    ssh_port INTEGER NOT NULL DEFAULT 22,
    ssh_key_path TEXT,
    cpu INTEGER,
    memory BIGINT,
    gpu VARCHAR(255),
    gpu_count INTEGER,
    hostname VARCHAR(255),
    os VARCHAR(255),
    ssh_status discovery_status NOT NULL DEFAULT 'pending',
    error_msg TEXT,
    scanned_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    CONSTRAINT unique_discovery_ip UNIQUE (ip_address)
);

-- Create machine events table
CREATE TABLE IF NOT EXISTS machine_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id UUID NOT NULL,
    event_type VARCHAR(50) NOT NULL,
    old_state machine_state,
    new_state machine_state,
    message TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_machine
        FOREIGN KEY(machine_id)
        REFERENCES machines(id)
        ON DELETE CASCADE
);

-- Create indexes
CREATE INDEX IF NOT EXISTS idx_machines_state ON machines(state);
CREATE INDEX IF NOT EXISTS idx_machines_nodepool ON machines(nodepool_id);
CREATE INDEX IF NOT EXISTS idx_machines_ip ON machines(ip_address);
CREATE INDEX IF NOT EXISTS idx_machines_labels ON machines USING GIN(labels);
CREATE INDEX IF NOT EXISTS idx_nodepools_state ON nodepools(state);
CREATE INDEX IF NOT EXISTS idx_discovery_results_status ON discovery_results(ssh_status);
CREATE INDEX IF NOT EXISTS idx_machine_events_machine ON machine_events(machine_id);
CREATE INDEX IF NOT EXISTS idx_machine_events_created ON machine_events(created_at);

-- Create trigger for updated_at
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

DO $$
DECLARE
    t text;
BEGIN
    FOR t IN
        SELECT table_name
        FROM information_schema.columns
        WHERE column_name = 'updated_at'
          AND table_schema = 'public'
    LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS update_%I_updated_at ON %I', t, t);
        EXECUTE format('CREATE TRIGGER update_%I_updated_at BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()', t, t);
    END LOOP;
END;
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS update_machines_updated_at ON machines;
DROP TRIGGER IF EXISTS update_nodepools_updated_at ON nodepools;
DROP TRIGGER IF EXISTS update_discovery_results_updated_at ON discovery_results;
DROP TRIGGER IF EXISTS update_machine_events_updated_at ON machine_events;

DROP FUNCTION IF EXISTS update_updated_at_column();

DROP TABLE IF EXISTS machine_events;
DROP TABLE IF EXISTS discovery_results;
DROP TABLE IF EXISTS machines;
DROP TABLE IF EXISTS nodepools;

DROP TYPE IF EXISTS machine_state CASCADE;
DROP TYPE IF EXISTS nodepool_state CASCADE;
DROP TYPE IF EXISTS discovery_status CASCADE;

-- +goose StatementEnd
