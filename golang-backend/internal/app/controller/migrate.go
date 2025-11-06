package controller

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// POST /api/v1/migrate {host, port, user, password, db}
func MigrateFrom(c *gin.Context) {
	var p struct {
		Host     string `json:"host" binding:"required"`
		Port     string `json:"port" binding:"required"`
		User     string `json:"user" binding:"required"`
		Password string `json:"password"`
		DBName   string `json:"db" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s", p.User, p.Password, p.Host, p.Port, p.DBName)
	src, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("连接源数据库失败"))
		return
	}

	// migrate each table
	stats, err := copyAll(src, dbpkg.DB)
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("迁移失败: "+err.Error()))
		return
	}
	c.JSON(http.StatusOK, response.Ok(map[string]any{"tables": stats}))
}

type tableStat struct {
	Table    string `json:"table"`
	SrcCount int64  `json:"srcCount"`
	Inserted int64  `json:"inserted"`
}

func copyAll(src *gorm.DB, dst *gorm.DB) ([]tableStat, error) {
	out := make([]tableStat, 0, 8)
	// order matters due to relations
	if st, err := copyTable[model.User](src, dst, "user"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.Node](src, dst, "node"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.Tunnel](src, dst, "tunnel"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.Forward](src, dst, "forward"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.UserTunnel](src, dst, "user_tunnel"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.SpeedLimit](src, dst, "speed_limit"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.ViteConfig](src, dst, "vite_config"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.StatisticsFlow](src, dst, "statistics_flow"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	return out, nil
}

func copyTable[T any](src *gorm.DB, dst *gorm.DB, table string) (tableStat, error) {
	st := tableStat{Table: table}
	if err := src.Model(new(T)).Count(&st.SrcCount).Error; err != nil {
		return st, err
	}
	if st.SrcCount == 0 {
		return st, nil
	}
	var list []T
	if err := src.Find(&list).Error; err != nil {
		return st, err
	}
	if len(list) == 0 {
		return st, nil
	}
	if err := dst.Create(&list).Error; err != nil {
		return st, err
	}
	st.Inserted = int64(len(list))
	return st, nil
}

// POST /api/v1/migrate/test {host, port, user, password, db}
// return basic connectivity and per-table counts
func MigrateTest(c *gin.Context) {
	var p struct {
		Host     string `json:"host" binding:"required"`
		Port     string `json:"port" binding:"required"`
		User     string `json:"user" binding:"required"`
		Password string `json:"password"`
		DBName   string `json:"db" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s", p.User, p.Password, p.Host, p.Port, p.DBName)
	src, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("连接失败"))
		return
	}
	var (
		userCount           int64
		nodeCount           int64
		tunnelCount         int64
		forwardCount        int64
		userTunnelCount     int64
		speedLimitCount     int64
		viteConfigCount     int64
		statisticsFlowCount int64
	)
	counts := map[string]int64{}

	_ = src.Model(&model.User{}).Count(&userCount).Error
	counts["user"] = userCount
	/*
		_ = src.Model(&model.Node{}).Count(&counts["node"]).Error
			_ = src.Model(&model.Tunnel{}).Count(&counts["tunnel"]).Error
			_ = src.Model(&model.Forward{}).Count(&counts["forward"]).Error
			_ = src.Model(&model.UserTunnel{}).Count(&counts["user_tunnel"]).Error
			_ = src.Model(&model.SpeedLimit{}).Count(&counts["speed_limit"]).Error
			_ = src.Model(&model.ViteConfig{}).Count(&counts["vite_config"]).Error
			_ = src.Model(&model.StatisticsFlow{}).Count(&counts["statistics_flow"]).Error
	*/
	_ = src.Model(&model.Node{}).Count(&nodeCount).Error
	counts["node"] = nodeCount
	_ = src.Model(&model.Tunnel{}).Count(&tunnelCount).Error
	counts["tunnel"] = tunnelCount
	_ = src.Model(&model.Forward{}).Count(&forwardCount).Error
	counts["forward"] = forwardCount
	_ = src.Model(&model.UserTunnel{}).Count(&userTunnelCount).Error
	counts["user_tunnel"] = userTunnelCount
	_ = src.Model(&model.SpeedLimit{}).Count(&speedLimitCount).Error
	counts["speed_limit"] = speedLimitCount
	_ = src.Model(&model.ViteConfig{}).Count(&viteConfigCount).Error
	counts["vite_config"] = viteConfigCount
	_ = src.Model(&model.StatisticsFlow{}).Count(&statisticsFlowCount).Error
	counts["statistics_flow"] = statisticsFlowCount
	
	c.JSON(http.StatusOK, response.Ok(map[string]any{"ok": true, "counts": counts}))
}

// ========== Progress variant ==========

type migProgress struct {
	JobID     string      `json:"jobId"`
	StartedAt int64       `json:"startedAt"`
	UpdatedAt int64       `json:"updatedAt"`
	Status    string      `json:"status"` // running, done, error
	Error     string      `json:"error,omitempty"`
	Tables    []tableStat `json:"tables"`
	Current   int         `json:"current"`
	Total     int         `json:"total"`
}

var (
	migMu   sync.Mutex
	migJobs = map[string]*migProgress{}
)

// POST /api/v1/migrate/start
func MigrateStart(c *gin.Context) {
	var p struct {
		Host     string `json:"host" binding:"required"`
		Port     string `json:"port" binding:"required"`
		User     string `json:"user" binding:"required"`
		Password string `json:"password"`
		DBName   string `json:"db" binding:"required"`
	}
	if err := c.ShouldBindJSON(&p); err != nil {
		c.JSON(http.StatusOK, response.ErrMsg("参数错误"))
		return
	}
	job := &migProgress{JobID: fmt.Sprintf("job_%d", time.Now().UnixNano()), StartedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(), Status: "running", Total: 8}
	migMu.Lock()
	migJobs[job.JobID] = job
	migMu.Unlock()
	go func() {
		defer func() { job.UpdatedAt = time.Now().UnixMilli() }()
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s", p.User, p.Password, p.Host, p.Port, p.DBName)
		src, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
		if err != nil {
			job.Status = "error"
			job.Error = "连接源数据库失败"
			return
		}
		runWithProgress(src, dbpkg.DB, job)
	}()
	c.JSON(http.StatusOK, response.Ok(map[string]any{"jobId": job.JobID}))
}

// GET /api/v1/migrate/status?jobId=...
func MigrateStatus(c *gin.Context) {
	id := c.Query("jobId")
	migMu.Lock()
	job := migJobs[id]
	migMu.Unlock()
	if job == nil {
		c.JSON(http.StatusOK, response.ErrMsg("job not found"))
		return
	}
	c.JSON(http.StatusOK, response.Ok(job))
}

func runWithProgress(src *gorm.DB, dst *gorm.DB, job *migProgress) {
	update := func() { job.UpdatedAt = time.Now().UnixMilli() }
	do := func(table string, fn func() (tableStat, error)) bool {
		st, err := fn()
		if err != nil {
			job.Status = "error"
			job.Error = table + ":" + err.Error()
			update()
			return false
		}
		job.Tables = append(job.Tables, st)
		job.Current = len(job.Tables)
		update()
		return true
	}
	seq := []struct {
		name string
		fn   func() (tableStat, error)
	}{
		{"user", func() (tableStat, error) { return copyTable[model.User](src, dst, "user") }},
		{"node", func() (tableStat, error) { return copyTable[model.Node](src, dst, "node") }},
		{"tunnel", func() (tableStat, error) { return copyTable[model.Tunnel](src, dst, "tunnel") }},
		{"forward", func() (tableStat, error) { return copyTable[model.Forward](src, dst, "forward") }},
		{"user_tunnel", func() (tableStat, error) { return copyTable[model.UserTunnel](src, dst, "user_tunnel") }},
		{"speed_limit", func() (tableStat, error) { return copyTable[model.SpeedLimit](src, dst, "speed_limit") }},
		{"vite_config", func() (tableStat, error) { return copyTable[model.ViteConfig](src, dst, "vite_config") }},
		{"statistics_flow", func() (tableStat, error) { return copyTable[model.StatisticsFlow](src, dst, "statistics_flow") }},
	}
	for _, step := range seq {
		if !do(step.name, step.fn) {
			return
		}
	}
	job.Status = "done"
	update()
}
