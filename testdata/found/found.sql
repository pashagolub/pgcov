-- FOUND must survive instrumentation: a signal injected between PERFORM and
-- IF FOUND used to overwrite it.
CREATE OR REPLACE FUNCTION found_after_perform(hit boolean) RETURNS boolean AS $$
BEGIN
    PERFORM 1 WHERE hit;
    IF FOUND THEN
        RETURN true;
    END IF;
    RETURN false;
END;
$$ LANGUAGE plpgsql;
