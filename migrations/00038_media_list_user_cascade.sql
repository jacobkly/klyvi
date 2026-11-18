-- media_list.user_id was nullable and did not cascade, unlike every other
-- table that belongs to a user. A list row with no user is unreachable, so
-- the column becomes required, and deleting a user removes their list.
-- SET NOT NULL fails if any row has a null user_id. Check first with
--   select count(*) from media_list where user_id is null;

-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_list ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE media_list DROP CONSTRAINT media_list_user_id_fkey;
ALTER TABLE media_list
    ADD CONSTRAINT media_list_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_list DROP CONSTRAINT media_list_user_id_fkey;
ALTER TABLE media_list
    ADD CONSTRAINT media_list_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE media_list ALTER COLUMN user_id DROP NOT NULL;
-- +goose StatementEnd
