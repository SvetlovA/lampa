-- +goose Up
alter table lampa_user_data
    drop column encrypted_connections,
    add column etag text not null default '';

-- +goose Down
alter table lampa_user_data
    drop column etag,
    add column encrypted_connections bytea null;
