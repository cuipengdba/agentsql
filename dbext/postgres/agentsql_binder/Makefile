EXTENSION = agentsql_binder
MODULE_big = agentsql_binder
OBJS = agentsql_binder.o
DATA = agentsql_binder--0.4.sql
PGFILEDESC = "agentsql_binder - analyzed OID binder for AgentSQL"

PG_CONFIG = pg_config
PGXS := $(shell $(PG_CONFIG) --pgxs)
include $(PGXS)
