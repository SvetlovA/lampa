-- +goose Up
create table lampa_user_data (
    user_id uuid primary key,
    schema_version integer not null,
    data jsonb not null,
    encrypted_connections bytea null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

-- +goose Down
drop table lampa_user_data;
