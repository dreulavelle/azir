-- How long an answer may be.
--
-- Configurable because it is a budget decision, not a technical one: some
-- providers reserve the whole allowance up front and refuse a request that
-- asks for more than the key can currently afford, so a deployment on a tight
-- weekly limit needs to be able to ask for less.
ALTER TABLE assistant_config
    ADD COLUMN max_answer_tokens INT NOT NULL DEFAULT 4096
        CHECK (max_answer_tokens BETWEEN 256 AND 32000);
