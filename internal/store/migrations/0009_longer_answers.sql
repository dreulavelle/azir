-- A more generous default answer length.
--
-- 4096 was chosen when the only provider was one whose replies are short by
-- habit. Current models routinely write far more, and an answer that stops
-- mid-sentence is worse than one nobody reads to the end — so the default
-- moves up and the ceiling moves well clear of it.
ALTER TABLE assistant_config
    ALTER COLUMN max_answer_tokens SET DEFAULT 16384;

ALTER TABLE assistant_config
    DROP CONSTRAINT assistant_config_max_answer_tokens_check;

ALTER TABLE assistant_config
    ADD CONSTRAINT assistant_config_max_answer_tokens_check
        CHECK (max_answer_tokens BETWEEN 256 AND 200000);

-- Existing deployments still sitting on the old default move with it. A value
-- somebody deliberately chose is left alone.
UPDATE assistant_config SET max_answer_tokens = 16384 WHERE max_answer_tokens = 4096;
