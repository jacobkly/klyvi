-- +goose Up
-- +goose StatementBegin
-- These tables came after the lockdown in 00021, so only its default-privilege
-- revoke keeps the anon key out of them. Same deny-by-default posture as every
-- other table: RLS on with no policies. Never add FORCE, see 00021.
ALTER TABLE public.imports ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.feature_stats ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.feature_corpus ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.feature_corpus DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.feature_stats DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.imports DISABLE ROW LEVEL SECURITY;
-- +goose StatementEnd
