package controller

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"network-panel/golang-backend/internal/app/model"
	"network-panel/golang-backend/internal/app/response"
	dbpkg "network-panel/golang-backend/internal/db"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	Table     string `json:"table"`
	SrcCount  int64  `json:"srcCount"`
	Inserted  int64  `json:"inserted"`
	Status    string `json:"status,omitempty"` // pending|running|done|error
	StartedAt int64  `json:"startedAt,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	EtaSec    *int64 `json:"etaSec,omitempty"`
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
	if st, err := copyTable[model.UserTunnel](src, dst, "user_tunnel"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.UserNode](src, dst, "user_node"); err != nil {
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
	if st, err := copyTable[model.ForwardMidPort](src, dst, "forward_mid_port"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.ExitSetting](src, dst, "exit_setting"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.AnyTLSSetting](src, dst, "anytls_setting"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.ExitNodeExternal](src, dst, "exit_node_external"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.ProbeTarget](src, dst, "probe_target"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.NodeSysInfo](src, dst, "node_sysinfo"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.NodeRuntime](src, dst, "node_runtime"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.EasyTierResult](src, dst, "easytier_result"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.StatisticsFlow](src, dst, "statistics_flow"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.FlowTimeseries](src, dst, "flow_timeseries"); err != nil {
		return out, err
	} else {
		out = append(out, st)
	}
	if st, err := copyTable[model.NQResult](src, dst, "nq_result"); err != nil {
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
    // Use upsert semantics to avoid duplicate primary key errors when destination已有数据
    // For MySQL: INSERT ... ON DUPLICATE KEY UPDATE ...
    // For SQLite: INSERT ... ON CONFLICT(id) DO UPDATE SET ...
    if err := dst.Clauses(clause.OnConflict{UpdateAll: true}).Create(&list).Error; err != nil {
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
		userNodeCount       int64
		speedLimitCount     int64
		viteConfigCount     int64
		statisticsFlowCount int64
		exitSettingCount    int64
		anytlsSettingCount  int64
		exitExternalCount   int64
		forwardMidPortCount int64
		probeTargetCount    int64
		nodeSysinfoCount    int64
		nodeRuntimeCount    int64
		nodeOpLogCount      int64
		nodeDiagCount       int64
		easyTierCount       int64
		flowTimeseriesCount int64
		nqResultCount       int64
		heartbeatCount      int64
		nodeDisconnectCount int64
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
	_ = src.Model(&model.UserNode{}).Count(&userNodeCount).Error
	counts["user_node"] = userNodeCount
	_ = src.Model(&model.SpeedLimit{}).Count(&speedLimitCount).Error
	counts["speed_limit"] = speedLimitCount
	_ = src.Model(&model.ViteConfig{}).Count(&viteConfigCount).Error
	counts["vite_config"] = viteConfigCount
	_ = src.Model(&model.StatisticsFlow{}).Count(&statisticsFlowCount).Error
	counts["statistics_flow"] = statisticsFlowCount
	_ = src.Model(&model.ExitSetting{}).Count(&exitSettingCount).Error
	counts["exit_setting"] = exitSettingCount
	_ = src.Model(&model.AnyTLSSetting{}).Count(&anytlsSettingCount).Error
	counts["anytls_setting"] = anytlsSettingCount
	_ = src.Model(&model.ExitNodeExternal{}).Count(&exitExternalCount).Error
	counts["exit_node_external"] = exitExternalCount
	_ = src.Model(&model.ForwardMidPort{}).Count(&forwardMidPortCount).Error
	counts["forward_mid_port"] = forwardMidPortCount
	_ = src.Model(&model.ProbeTarget{}).Count(&probeTargetCount).Error
	counts["probe_target"] = probeTargetCount
	_ = src.Model(&model.NodeSysInfo{}).Count(&nodeSysinfoCount).Error
	counts["node_sysinfo"] = nodeSysinfoCount
	_ = src.Model(&model.NodeRuntime{}).Count(&nodeRuntimeCount).Error
	counts["node_runtime"] = nodeRuntimeCount
	_ = src.Model(&model.NodeOpLog{}).Count(&nodeOpLogCount).Error
	_ = src.Model(&model.NodeDiagResult{}).Count(&nodeDiagCount).Error
	_ = src.Model(&model.EasyTierResult{}).Count(&easyTierCount).Error
	counts["easytier_result"] = easyTierCount
	_ = src.Model(&model.FlowTimeseries{}).Count(&flowTimeseriesCount).Error
	counts["flow_timeseries"] = flowTimeseriesCount
	_ = src.Model(&model.NQResult{}).Count(&nqResultCount).Error
	counts["nq_result"] = nqResultCount
	_ = src.Model(&model.HeartbeatRecord{}).Count(&heartbeatCount).Error
	_ = src.Model(&model.NodeDisconnectLog{}).Count(&nodeDisconnectCount).Error
	
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
	job := &migProgress{JobID: fmt.Sprintf("job_%d", time.Now().UnixNano()), StartedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(), Status: "running", Total: 0}
	migMu.Lock()
	migJobs[job.JobID] = job
	migMu.Unlock()
	go func() {
		defer func() { job.UpdatedAt = time.Now().UnixMilli() }()
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s", p.User, p.Password, p.Host, p.Port, p.DBName)
		srcProvider := makeSrcProvider(dsn)
		if _, err := srcProvider(); err != nil {
			job.Status = "error"
			job.Error = "连接源数据库失败"
			return
		}
		runWithProgress(srcProvider, dbpkg.DB, job)
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

func runWithProgress(srcProvider func() (*gorm.DB, error), dst *gorm.DB, job *migProgress) {
	update := func() { job.UpdatedAt = time.Now().UnixMilli() }
	seq := []struct {
		name string
		count func(*tableStat) error
		fn    func(*tableStat) error
	}{
		{"user", func(st *tableStat) error { return countTable[model.User](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.User](srcProvider, dst, st, update) }},
		{"node", func(st *tableStat) error { return countTable[model.Node](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.Node](srcProvider, dst, st, update) }},
		{"user_tunnel", func(st *tableStat) error { return countTable[model.UserTunnel](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.UserTunnel](srcProvider, dst, st, update) }},
		{"user_node", func(st *tableStat) error { return countTable[model.UserNode](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.UserNode](srcProvider, dst, st, update) }},
		{"speed_limit", func(st *tableStat) error { return countTable[model.SpeedLimit](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.SpeedLimit](srcProvider, dst, st, update) }},
		{"vite_config", func(st *tableStat) error { return countTable[model.ViteConfig](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.ViteConfig](srcProvider, dst, st, update) }},
		{"tunnel", func(st *tableStat) error { return countTable[model.Tunnel](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.Tunnel](srcProvider, dst, st, update) }},
		{"forward", func(st *tableStat) error { return countTable[model.Forward](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.Forward](srcProvider, dst, st, update) }},
		{"forward_mid_port", func(st *tableStat) error { return countTable[model.ForwardMidPort](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.ForwardMidPort](srcProvider, dst, st, update) }},
		{"exit_setting", func(st *tableStat) error { return countTable[model.ExitSetting](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.ExitSetting](srcProvider, dst, st, update) }},
		{"anytls_setting", func(st *tableStat) error { return countTable[model.AnyTLSSetting](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.AnyTLSSetting](srcProvider, dst, st, update) }},
		{"exit_node_external", func(st *tableStat) error { return countTable[model.ExitNodeExternal](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.ExitNodeExternal](srcProvider, dst, st, update) }},
		{"probe_target", func(st *tableStat) error { return countTable[model.ProbeTarget](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.ProbeTarget](srcProvider, dst, st, update) }},
		{"node_sysinfo", func(st *tableStat) error { return countTable[model.NodeSysInfo](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.NodeSysInfo](srcProvider, dst, st, update) }},
		{"node_runtime", func(st *tableStat) error { return countTable[model.NodeRuntime](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.NodeRuntime](srcProvider, dst, st, update) }},
		{"easytier_result", func(st *tableStat) error { return countTable[model.EasyTierResult](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.EasyTierResult](srcProvider, dst, st, update) }},
		{"statistics_flow", func(st *tableStat) error { return countTable[model.StatisticsFlow](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.StatisticsFlow](srcProvider, dst, st, update) }},
		{"flow_timeseries", func(st *tableStat) error { return countTable[model.FlowTimeseries](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.FlowTimeseries](srcProvider, dst, st, update) }},
		{"nq_result", func(st *tableStat) error { return countTable[model.NQResult](srcProvider, st) }, func(st *tableStat) error { return copyTableWithProgress[model.NQResult](srcProvider, dst, st, update) }},
	}
	job.Total = len(seq)
	job.Tables = make([]tableStat, 0, len(seq))
	index := map[string]int{}
	now := time.Now().UnixMilli()
	for i, step := range seq {
		job.Tables = append(job.Tables, tableStat{Table: step.name, Status: "pending", StartedAt: 0, UpdatedAt: now})
		index[step.name] = i
	}
	update()
	for _, step := range seq {
		st := &job.Tables[index[step.name]]
		st.Status = "running"
		st.StartedAt = time.Now().UnixMilli()
		st.UpdatedAt = st.StartedAt
		update()
		if err := step.count(st); err == nil {
			st.UpdatedAt = time.Now().UnixMilli()
			update()
		}
		if err := step.fn(st); err != nil {
			st.Status = "error"
			st.UpdatedAt = time.Now().UnixMilli()
			job.Status = "error"
			job.Error = step.name + ":" + err.Error()
			update()
			return
		}
		st.Status = "done"
		st.UpdatedAt = time.Now().UnixMilli()
		st.EtaSec = int64Ptr(0)
		job.Current++
		update()
	}
	job.Status = "done"
	update()
}

func int64Ptr(v int64) *int64 { return &v }

func countTable[T any](srcProvider func() (*gorm.DB, error), st *tableStat) error {
	src, err := srcProvider()
	if err != nil {
		return err
	}
	if err := src.Model(new(T)).Count(&st.SrcCount).Error; err != nil {
		if isBadConn(err) {
			if src, err = srcProvider(); err != nil {
				return err
			}
			return src.Model(new(T)).Count(&st.SrcCount).Error
		}
		return err
	}
	return nil
}

func copyTableWithProgress[T any](srcProvider func() (*gorm.DB, error), dst *gorm.DB, st *tableStat, tick func()) error {
	if st.SrcCount == 0 {
		src, err := srcProvider()
		if err != nil {
			return err
		}
		if err := src.Model(new(T)).Count(&st.SrcCount).Error; err != nil {
			return err
		}
	}
	if st.SrcCount == 0 {
		st.Inserted = 0
		st.EtaSec = int64Ptr(0)
		st.UpdatedAt = time.Now().UnixMilli()
		tick()
		return nil
	}
	const batch = 500
	var inserted int64
	start := time.Now()
	for offset := 0; offset < int(st.SrcCount); offset += batch {
		var list []T
		var src *gorm.DB
		var err error
		if src, err = srcProvider(); err != nil {
			return err
		}
		if err = src.Limit(batch).Offset(offset).Find(&list).Error; err != nil {
			if isBadConn(err) {
				if src, err = srcProvider(); err != nil {
					return err
				}
				if err = src.Limit(batch).Offset(offset).Find(&list).Error; err != nil {
					return err
				}
			} else {
				return err
			}
		}
		if len(list) == 0 {
			break
		}
		if err := dst.Clauses(clause.OnConflict{UpdateAll: true}).Create(&list).Error; err != nil {
			if isBadConn(err) {
				if err = dst.Clauses(clause.OnConflict{UpdateAll: true}).Create(&list).Error; err != nil {
					return err
				}
			} else {
				return err
			}
		}
		inserted += int64(len(list))
		st.Inserted = inserted
		st.UpdatedAt = time.Now().UnixMilli()
		if inserted > 0 {
			elapsed := time.Since(start).Seconds()
			if elapsed > 0 && inserted < st.SrcCount {
				rate := float64(inserted) / elapsed
				if rate > 0 {
					eta := int64(float64(st.SrcCount-inserted) / rate)
					st.EtaSec = &eta
				}
			}
		}
		tick()
	}
	return nil
}

func isBadConn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "bad connection") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset")
}

func openSrcDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if sqlDB, err2 := db.DB(); err2 == nil {
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetMaxOpenConns(10)
		sqlDB.SetConnMaxLifetime(10 * time.Minute)
		sqlDB.SetConnMaxIdleTime(2 * time.Minute)
	}
	return db, nil
}

func makeSrcProvider(dsn string) func() (*gorm.DB, error) {
	var mu sync.Mutex
	var cached *gorm.DB
	return func() (*gorm.DB, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != nil {
			if sqlDB, err := cached.DB(); err == nil {
				if err = sqlDB.Ping(); err == nil {
					return cached, nil
				}
			}
		}
		db, err := openSrcDB(dsn)
		if err != nil {
			return nil, err
		}
		cached = db
		return cached, nil
	}
}
