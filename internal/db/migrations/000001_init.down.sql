-- 回滚初始 schema：反序删除（先删引用方）。
DROP TABLE IF EXISTS manifest_head;
DROP TABLE IF EXISTS manifests;
DROP TABLE IF EXISTS blocks;
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS accounts;
