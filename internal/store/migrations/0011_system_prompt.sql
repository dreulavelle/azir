-- The system prompt, in the open.
--
-- Azir shipped with a fixed prompt and an "added instructions" box on the end.
-- That kept the containment sentences safe from being edited, which sounds
-- prudent and is mostly theatre: the assistant is never handed a tool that
-- writes and the runner checks again before performing anything, so deleting a
-- sentence cannot grant a capability. What the arrangement actually cost was
-- the ability to read what your own assistant was told, which is a fair thing
-- for an operator to want and a necessary thing for anyone auditing it.
--
-- Empty means "use whatever this version of Azir ships", so an upgrade that
-- improves the wording reaches every deployment that has not overridden it.
ALTER TABLE assistant_config
    ADD COLUMN system_prompt TEXT NOT NULL DEFAULT ''
        CHECK (length(system_prompt) <= 20000);
