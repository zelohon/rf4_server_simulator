-- Hotkey aliases are references; physical positions remain unique.
CREATE TRIGGER clear_deleted_shortcut_aliases AFTER DELETE ON item
BEGIN
    DELETE FROM container_slot WHERE uid=OLD.uid AND guid=OLD.guid;
    UPDATE kv SET v=(SELECT COALESCE(json_group_object(key,value),'{}')
                     FROM json_each(kv.v) WHERE value<>OLD.guid)
    WHERE k='shortcut-aliases:' || OLD.uid AND json_valid(v);
END;

CREATE TRIGGER clear_unavailable_shortcut_aliases AFTER UPDATE OF uid,container ON item
WHEN NEW.uid<>OLD.uid OR NEW.container NOT IN ('backpack','rig')
BEGIN
    DELETE FROM container_slot WHERE uid=OLD.uid AND guid=OLD.guid;
    UPDATE kv SET v=(SELECT COALESCE(json_group_object(key,value),'{}')
                     FROM json_each(kv.v) WHERE value<>OLD.guid)
    WHERE k='shortcut-aliases:' || OLD.uid AND json_valid(v);
END;
