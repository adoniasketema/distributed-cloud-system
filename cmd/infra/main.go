package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
)

func main() {
	fmt.Println("[INFO] Verifying infrastructure connectivity...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Test PostgreSQL Connection
	if err := testPostgres(ctx); err != nil {
		log.Fatalf("[FAIL] PostgreSQL connection error: %v", err)
	}
	fmt.Println("[PASS] PostgreSQL connectivity verified.")

	// 2. Test MinIO Connection
	if err := testMinio(ctx); err != nil {
		log.Fatalf("[FAIL] MinIO object store connection error: %v", err)
	}
	fmt.Println("[PASS] MinIO object store connectivity verified.")

	// 3. Test Redis Connection
	if err := testRedis(ctx); err != nil {
		log.Fatalf("[FAIL] Redis message broker connection error: %v", err)
	}
	fmt.Println("[PASS] Redis message broker connectivity verified.")

	fmt.Println("[SUCCESS] All infrastructure dependencies are operational and reachable.")
}

func testPostgres(ctx context.Context) error {
	connStr := "postgres://nimbus:password@localhost:5432/nimbus_db"
	conn, err := pgx.Connect(ctx, connStr)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	var greeting string
	return conn.QueryRow(ctx, "select 'Hello from PostgreSQL'").Scan(&greeting)
}

func testMinio(ctx context.Context) error {
	endpoint := "localhost:9000"
	accessKeyID := "minioadmin"
	secretAccessKey := "minioadmin"
	useSSL := false

	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return err
	}

	_, err = minioClient.ListBuckets(ctx)
	return err
}

func testRedis(ctx context.Context) error {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	_, err := rdb.Ping(ctx).Result()
	return err
}
