-- +goose Up
-- +goose StatementBegin
-- co_liked holds, for each title, the titles Klyvi users most often liked
-- along with it. It is derived data: the nightly /coliked job deletes and
-- rebuilds every row from interactions, so it has no foreign keys. Both ids are
-- media_index ids, seasons rolled up to their series. users is how many users
-- liked both titles, similarity the cosine of the two titles' sets of likers.
CREATE TABLE co_liked (
    media_id   INTEGER          NOT NULL,
    other_id   INTEGER          NOT NULL,
    users      INTEGER          NOT NULL CHECK (users > 0),
    similarity DOUBLE PRECISION NOT NULL CHECK (similarity > 0),
    PRIMARY KEY (media_id, other_id)
);

-- Same deny-by-default posture as every other table: RLS on with no policies.
-- Never add FORCE, see 00021.
ALTER TABLE public.co_liked ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE co_liked;
-- +goose StatementEnd
