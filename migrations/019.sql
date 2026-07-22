-- 下载次数只在服务端确认整个下载任务的所有字节均已成功发送后提交。
ALTER TABLE share_download_jobs RENAME COLUMN used_at TO reserved_at;
ALTER TABLE share_download_jobs ADD COLUMN completed_at TIMESTAMPTZ;
ALTER TABLE share_download_job_files DROP COLUMN used_at;

CREATE TABLE share_download_job_ranges (
    job_id      BIGINT NOT NULL REFERENCES share_download_jobs(id) ON DELETE CASCADE,
    object_key  TEXT NOT NULL,
    range_start BIGINT NOT NULL,
    range_end   BIGINT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (job_id, object_key, range_start, range_end),
    CONSTRAINT share_download_job_ranges_valid CHECK (range_start >= 0 AND range_end >= range_start)
);

CREATE INDEX share_download_job_ranges_job_idx ON share_download_job_ranges(job_id, object_key, range_start);
