## codeinsights-db Docker Image

We republish the TimescaleDB (open source) Docker image under sourcegraph/codeinsights-db to ensure it uses our standard naming and versioning scheme. This is done in `docker-images/codeinsights-db/`.

## Getting a psql prompt (dev server)

```sh
docker exec -it codeinsights-db psql -U postgres
```

## Migrations

Since TimescaleDB is just Postgres (with an extension), we use the same SQL migration framework we use for our other Postgres databases. `migrations/codeinsights` contains the migrations for the Code Insights database, they are executed when the frontend starts up (as is the same with e.g. codeintel DB migrations.)

### Add a new migration

To add a new migration, use:

```
./dev/db/add_migration.sh codeinsights MIGRATION_NAME
```

See [migrations/README.md](migrations/README.md) for more information

# Random stuff

## Upsert repo names

```
WITH e AS(
    INSERT INTO repo_names(name)
    VALUES ('github.com/gorilla/mux-original')
    ON CONFLICT DO NOTHING
    RETURNING id
)
SELECT * FROM e
UNION
    SELECT id FROM repo_names WHERE name='github.com/gorilla/mux-original';

WITH e AS(
    INSERT INTO repo_names(name)
    VALUES ('github.com/gorilla/mux-renamed')
    ON CONFLICT DO NOTHING
    RETURNING id
)
SELECT * FROM e
UNION
    SELECT id FROM repo_names WHERE name='github.com/gorilla/mux-renamed';
```

## Upsert event metadata

Upsert metadata, getting back ID:

```
WITH e AS(
    INSERT INTO metadata(metadata)
    VALUES ('{"hello": "world", "languages": ["Go", "Python", "Java"]}')
    ON CONFLICT DO NOTHING
    RETURNING id
)
SELECT * FROM e
UNION
    SELECT id FROM metadata WHERE metadata='{"hello": "world", "languages": ["Go", "Python", "Java"]}';
```

## Inserting gauge events

```
INSERT INTO series_points(
    time,
    value,
    metadata_id,
    repo_id,
    repo_name_id,
    original_repo_name_id
) VALUES(
    now(),
    0.5,
    (SELECT id FROM metadata WHERE metadata = '{"hello": "world", "languages": ["Go", "Python", "Java"]}'),
    2,
    (SELECT id FROM repo_names WHERE name = 'github.com/gorilla/mux-renamed'),
    (SELECT id FROM repo_names WHERE name = 'github.com/gorilla/mux-original')
);
```

## Inserting fake data

```
INSERT INTO series_points(
    time,
    value,
    metadata_id,
    repo_id,
    repo_name_id,
    original_repo_name_id)
SELECT time,
    random()*80 - 40,
    (SELECT id FROM metadata WHERE metadata = '{"hello": "world", "languages": ["Go", "Python", "Java"]}'),
    2,
    (SELECT id FROM repo_names WHERE name = 'github.com/gorilla/mux-renamed'),
    (SELECT id FROM repo_names WHERE name = 'github.com/gorilla/mux-original')
    FROM generate_series(TIMESTAMP '2020-01-01 00:00:00', TIMESTAMP '2020-06-01 00:00:00', INTERVAL '10 min') AS time;
```

## Querying all data

```
SELECT series_id,
	time,
	value,
	m.metadata,
	repo_id,
	repo_name.name,
	original_repo_name.name
FROM series_points p
INNER JOIN metadata m ON p.metadata_id = m.id
INNER JOIN repo_names repo_name on p.repo_name_id = repo_name.id
INNER JOIN repo_names original_repo_name on p.original_repo_name_id = original_repo_name.id
ORDER BY time DESC;
```

## Example Global Settings

```
  "insights": [
    {
      "title": "fmt usage",
      "description": "fmt.Errorf/fmt.Printf usage",
      "series": [
        {
          "label": "fmt.Errorf",
          "search": "errorf",
        },
        {
          "label": "printf",
          "search": "fmt.Printf",
        }
      ]
    }
  ]
```

## Query data

### All data

```
SELECT * FROM series_points ORDER BY time DESC LIMIT 100;
```

### Filter by repo name, returning metadata (may be more optimally queried separately)

```
SELECT *
FROM series_points
JOIN metadata ON metadata.id = metadata_id
WHERE repo_name_id IN (
    SELECT id FROM repo_names WHERE name ~ '.*-renamed'
)
ORDER BY time
DESC LIMIT 100;
```

### Filter by metadata containing `{"hello": "world"}`

```
SELECT *
FROM series_points
JOIN metadata ON metadata.id = metadata_id
WHERE metadata @> '{"hello": "world"}'
ORDER BY time
DESC LIMIT 100;
```

### Filter by metadata containing Go languages

```
SELECT *
FROM series_points
JOIN metadata ON metadata.id = metadata_id
WHERE metadata @> '{"languages": ["Go"]}'
ORDER BY time
DESC LIMIT 100;
```

See https://www.postgresql.org/docs/9.6/functions-json.html for more operator possibilities. Only ?, ?&, ?|, and @> operators are indexed (gin index)

### Get average/min/max value every 1h for

```
SELECT
    value,
    time_bucket(INTERVAL '1 hour', time) AS bucket,
    AVG(value),
    MAX(value),
    MIN(value)
FROM series_points
GROUP BY value, bucket;
```

Note: This is not optimized, we can use materialized views to do continuous aggregation.

See https://docs.timescale.com/latest/using-timescaledb/continuous-aggregates

## Why aren't insights being recorded?

Find insights background worker logs:

```
kubectl --namespace=prod logs repo-updater-76df6f4646-q92nx repo-updater | grep insights
```

## Get a psql prompt (Kubernetes)

```
kubectl -n prod exec -it codeinsights-db-5f5977f74d-8q9nl -- psql -U postgres
```
