package persistence

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/langgenius/dify-plugin-daemon/internal/db"
	"github.com/langgenius/dify-plugin-daemon/internal/types/models"
	"github.com/langgenius/dify-plugin-daemon/internal/utils/cache"
)

type Persistence struct {
	max_storage_size int64

	storage PersistenceStorage
}

const (
	CACHE_KEY_PREFIX = "persistence:cache"
)

func (c *Persistence) getCacheKey(tenant_id string, plugin_id string, key string) string {
	return fmt.Sprintf("%s:%s:%s:%s", CACHE_KEY_PREFIX, tenant_id, plugin_id, key)
}

func (c *Persistence) Save(tenant_id string, plugin_id string, max_size int64, key string, data []byte) error {
	if len(key) > 64 {
		return fmt.Errorf("key length must be less than 64 characters")
	}

	if max_size == -1 {
		max_size = c.max_storage_size
	}

	if err := c.storage.Save(tenant_id, plugin_id, key, data); err != nil {
		return err
	}

	allocated_size := int64(len(data))

	storage, err := db.GetOne[models.TenantStorage](
		db.Equal("tenant_id", tenant_id),
		db.Equal("plugin_id", plugin_id),
	)
	if err != nil {
		if allocated_size > c.max_storage_size || allocated_size > max_size {
			return fmt.Errorf("allocated size is greater than max storage size")
		}

		if err == db.ErrDatabaseNotFound {
			storage = models.TenantStorage{
				TenantID: tenant_id,
				PluginID: plugin_id,
				Size:     allocated_size,
			}
			if err := db.Create(&storage); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		if allocated_size+storage.Size > max_size || allocated_size+storage.Size > c.max_storage_size {
			return fmt.Errorf("allocated size is greater than max storage size")
		}

		err = db.Run(
			db.Model(&models.TenantStorage{}),
			db.Equal("tenant_id", tenant_id),
			db.Equal("plugin_id", plugin_id),
			db.Inc(map[string]int64{"size": allocated_size}),
		)
		if err != nil {
			return err
		}
	}

	// delete from cache
	return cache.Del(c.getCacheKey(tenant_id, plugin_id, key))
}

func (c *Persistence) Load(tenant_id string, plugin_id string, key string) ([]byte, error) {
	// check if the key exists in cache
	h, err := cache.GetString(c.getCacheKey(tenant_id, plugin_id, key))
	if err != nil && err != cache.ErrNotFound {
		return nil, err
	}
	if err == nil {
		return hex.DecodeString(h)
	}

	// load from storage
	data, err := c.storage.Load(tenant_id, plugin_id, key)
	if err != nil {
		return nil, err
	}

	// add to cache
	cache.Store(c.getCacheKey(tenant_id, plugin_id, key), hex.EncodeToString(data), time.Minute*5)

	return data, nil
}

func (c *Persistence) Delete(tenant_id string, plugin_id string, key string) error {
	// delete from cache and storage
	err := cache.Del(c.getCacheKey(tenant_id, plugin_id, key))
	if err != nil {
		return err
	}

	// state size
	size, err := c.storage.StateSize(tenant_id, plugin_id, key)
	if err != nil {
		return nil
	}

	err = c.storage.Delete(tenant_id, plugin_id, key)
	if err != nil {
		return nil
	}

	// update storage size
	err = db.Run(
		db.Model(&models.TenantStorage{}),
		db.Equal("tenant_id", tenant_id),
		db.Equal("plugin_id", plugin_id),
		db.Dec(map[string]int64{"size": size}),
	)
	if err != nil {
		return err
	}

	return nil
}
