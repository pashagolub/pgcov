-- Sorts before schema.sql but depends on it; only --source ordering can load it.
CREATE OR REPLACE FUNCTION app.double_it(x int) RETURNS int AS $$
BEGIN
    IF x IS NULL THEN
        RETURN 0;
    END IF;
    RETURN x * 2;
END;
$$ LANGUAGE 'plpgsql';
