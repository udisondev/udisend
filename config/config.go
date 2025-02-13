package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config хранит конфигурационные параметры приложения.
type Config struct {
	ID string
	Port     string
	Address  string
	Parent   string
	IsTURN   bool
	Cluster  Cluster
}

type Cluster struct {
	Size                int
	MinConnections      int
	MinCrossConnections int
}

// GetConfig загружает переменные окружения из .env файла (если он существует)
// и возвращает экземпляр структуры Config.
func GetConfig() *Config {
	// Пытаемся загрузить .env файл.
	if err := godotenv.Load(); err != nil {
		log.Fatal("Не удалось загрузить .env файл, переменные окружения берутся из системы")
	}

	clusterSize, err := strconv.Atoi(os.Getenv("CLUSTER_SIZE"))
	if err != nil {
		log.Fatalf("Error parse 'CLUSTER_SIZE' env: %v", err)
	}
	minClusterConnections, err := strconv.Atoi(os.Getenv("MIN_CLUSTER_CONNECTIONS"))
	if err != nil {
		log.Fatalf("Error parse 'MIN_CLUSTER_CONNECTIONS' env: %v", err)
	}
	minCrossClusterConnections, err := strconv.Atoi(os.Getenv("MIN_CROSS_CCLUSTER_CONNECTIONS"))
	if err != nil {
		log.Fatalf("Error parse 'MIN_CROSS_CCLUSTER_CONNECTIONS' env: %v", err)
	}

	config := &Config{
		ID: os.Getenv("NICK_NAME"),
		Port:     os.Getenv("PORT"),
		Parent:   os.Getenv("PARENT"),
		Address:  os.Getenv("ADDRESS"),
		Cluster: Cluster{
			Size:                clusterSize,
			MinConnections:      minClusterConnections,
			MinCrossConnections: minCrossClusterConnections,
		},
	}

	config.IsTURN = config.Parent == ""

	return config
}

func (c Config) GetPort() string {
	if c.Port == "" {
		return ":8000"
	}

	return ":" + c.Port
}

func (c Config) GetAddress() string {
	if c.IsTURN {
		return c.Address + c.GetPort()
	}

	return c.GetPort()
}
