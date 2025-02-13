# supabase

Supabase backend is used to create a leaderboard of connections made and people helped to drive donor engagement with the Unbounded widget.

Connect to Supabase with their [REST API](https://supabase.com/docs/guides/api).

## credentials
Can be fetched from the Supabase [dashboard](https://supabase.com/dashboard/project/_/settings/api). Save to env as:
- `SUPABASE_URL`
- `SUPABASE_KEY`

A `SUPABASE_KEY` can be (see [docs](https://supabase.com/docs/guides/api/api-keys)):
- anon (public, available to all)
- authenticated (logged in users)
- admin (full backend permissions)

## simplest example to the API
```sh
curl \
  $SUPABASE_URL/rest/v1/connections
  -H "apikey: $SUPABASE_KEY" -i
```

Will return a `200` (but an empty table) in the default JSON format.