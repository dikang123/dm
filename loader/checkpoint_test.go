package loader

import (
	"os"
	"strconv"

	. "github.com/pingcap/check"
	"github.com/pingcap/tidb-enterprise-tools/dm/config"
)

var _ = Suite(&testCheckPointSuite{})

type testCheckPointSuite struct {
	cfg *config.SubTaskConfig
}

func (t *testCheckPointSuite) SetUpSuite(c *C) {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port, _ := strconv.Atoi(os.Getenv("MYSQL_PORT"))
	if port == 0 {
		port = 3306
	}
	user := os.Getenv("MYSQL_USER")
	if user == "" {
		user = "root"
	}
	pswd := os.Getenv("MYSQL_PSWD")

	t.cfg = &config.SubTaskConfig{
		To: config.DBConfig{
			Host:     host,
			User:     user,
			Password: pswd,
			Port:     port,
		},
	}
}

func (t *testCheckPointSuite) TearDownSuite(c *C) {
}

// test checkpoint's db operation
func (t *testCheckPointSuite) TestForDB(c *C) {
	cases := []struct {
		filename string
		endPos   int64
	}{
		{"db1.tbl1.sql", 123},
		{"db1.tbl2.sql", 456},
		{"db1.tbl3.sql", 789},
	}

	id := "test_for_db"
	cp, err := newRemoteCheckPoint(t.cfg, id)
	c.Assert(err, IsNil)
	defer cp.Close()

	cp.Clear()

	// no checkpoint exist
	err = cp.Load()
	c.Assert(err, IsNil)

	infos := cp.GetAllRestoringFileInfo()
	c.Assert(len(infos), Equals, 0)

	count, err := cp.Count()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, 0)

	// insert default checkpoints
	for _, cs := range cases {
		err = cp.Init(cs.filename, cs.endPos)
		c.Assert(err, IsNil)
	}

	err = cp.Load()
	c.Assert(err, IsNil)

	infos = cp.GetAllRestoringFileInfo()
	c.Assert(len(infos), Equals, len(cases))
	for _, cs := range cases {
		info, ok := infos[cs.filename]
		c.Assert(ok, IsTrue)
		c.Assert(len(info), Equals, 2)
		c.Assert(info[0], Equals, int64(0))
		c.Assert(info[1], Equals, cs.endPos)
	}

	count, err = cp.Count()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, len(cases))

	// update checkpoints
	conn, err := createConn(t.cfg.To)
	c.Assert(err, IsNil)
	defer closeConn(conn)
	for _, cs := range cases {
		sql2 := cp.GenSQL(cs.filename, cs.endPos)
		err = executeSQL(conn, []string{sql2}, true)
		c.Assert(err, IsNil)
	}

	err = cp.Load()
	c.Assert(err, IsNil)

	infos = cp.GetAllRestoringFileInfo()
	c.Assert(len(infos), Equals, len(cases))
	for _, cs := range cases {
		info, ok := infos[cs.filename]
		c.Assert(ok, IsTrue)
		c.Assert(len(info), Equals, 2)
		c.Assert(info[0], Equals, cs.endPos)
		c.Assert(info[1], Equals, cs.endPos)
	}

	count, err = cp.Count()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, len(cases))

	// clear all
	cp.Clear()

	// no checkpoint exist
	err = cp.Load()
	c.Assert(err, IsNil)

	infos = cp.GetAllRestoringFileInfo()
	c.Assert(len(infos), Equals, 0)

	// obtain count again
	count, err = cp.Count()
	c.Assert(err, IsNil)
	c.Assert(count, Equals, 0)
}