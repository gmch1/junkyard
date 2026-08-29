package com.ming.lightprobe;

import android.content.Context;
import android.content.SharedPreferences;
import android.util.Log;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

/** Persistent device library. Built-in profiles and learned profiles share one model. */
public final class RemoteProfileStore {
    public static final String BUILTIN_FOSHAN_ID = "builtin-foshan-lighting";

    private static final String TAG = "REMOTE_PROFILES";
    private static final String PREFS = "remote_profile_store";
    private static final String KEY_PROFILES = "learned_profiles";
    private static final String KEY_ACTIVE = "active_profile";

    private RemoteProfileStore() {
    }

    public static List<RemoteProfile> list(Context context) {
        List<RemoteProfile> profiles = new ArrayList<>();
        profiles.add(builtinFoshan());
        SharedPreferences preferences = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        String stored = preferences.getString(KEY_PROFILES, "[]");
        try {
            JSONArray array = new JSONArray(stored);
            for (int i = 0; i < array.length(); i++) {
                try {
                    profiles.add(RemoteProfile.fromJson(array.getJSONObject(i)));
                } catch (JSONException | IllegalArgumentException invalidProfile) {
                    Log.w(TAG, "Ignoring invalid learned profile", invalidProfile);
                }
            }
        } catch (JSONException invalidStore) {
            Log.w(TAG, "Ignoring invalid profile store", invalidStore);
        }
        return profiles;
    }

    public static RemoteProfile active(Context context) {
        String activeId = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .getString(KEY_ACTIVE, BUILTIN_FOSHAN_ID);
        for (RemoteProfile profile : list(context)) {
            if (profile.id.equals(activeId) && profile.usable) {
                return profile;
            }
        }
        return builtinFoshan();
    }

    public static void activate(Context context, String profileId) {
        for (RemoteProfile profile : list(context)) {
            if (profile.id.equals(profileId) && profile.usable) {
                context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                        .edit().putString(KEY_ACTIVE, profileId).apply();
                return;
            }
        }
        throw new IllegalArgumentException("Unknown or unsupported profile: " + profileId);
    }

    public static RemoteProfile saveLearned(Context context,
            FanLampRemoteProtocol.Profile protocolProfile,
            JSONObject recordings) {
        List<RemoteProfile> existing = list(context);
        int learnedCount = 0;
        for (RemoteProfile profile : existing) {
            if (!profile.verified) {
                learnedCount++;
            }
        }
        long now = System.currentTimeMillis();
        RemoteProfile profile = new RemoteProfile(
                "learned-" + now,
                "录制遥控器 " + (learnedCount + 1),
                protocolProfile == null ? "raw-ble" : "fanlamp-v1",
                false,
                protocolProfile != null,
                now,
                protocolProfile,
                recordings == null ? new JSONObject() : recordings);

        JSONArray learned = new JSONArray();
        for (RemoteProfile value : existing) {
            if (!value.verified) {
                try {
                    learned.put(value.toJson());
                } catch (JSONException impossible) {
                    Log.e(TAG, "Unable to serialize existing profile", impossible);
                }
            }
        }
        try {
            learned.put(profile.toJson());
            context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                    .edit().putString(KEY_PROFILES, learned.toString()).apply();
        } catch (JSONException impossible) {
            throw new IllegalStateException("Unable to serialize learned profile", impossible);
        }
        return profile;
    }

    private static RemoteProfile builtinFoshan() {
        return new RemoteProfile(
                BUILTIN_FOSHAN_ID,
                "佛山照明吸顶灯",
                "fanlamp-v1",
                true,
                true,
                0L,
                FanLampRemoteProtocol.builtinProfile(),
                new JSONObject());
    }

    public static final class RemoteProfile {
        public final String id;
        public final String name;
        public final String protocol;
        public final boolean verified;
        public final boolean usable;
        public final long createdAt;
        public final FanLampRemoteProtocol.Profile fanLampProfile;
        public final JSONObject recordings;

        RemoteProfile(String id, String name, String protocol,
                boolean verified, boolean usable, long createdAt,
                FanLampRemoteProtocol.Profile fanLampProfile,
                JSONObject recordings) {
            this.id = id;
            this.name = name;
            this.protocol = protocol;
            this.verified = verified;
            this.usable = usable;
            this.createdAt = createdAt;
            this.fanLampProfile = fanLampProfile;
            this.recordings = recordings;
        }

        JSONObject toJson() throws JSONException {
            JSONObject value = new JSONObject();
            value.put("id", id);
            value.put("name", name);
            value.put("protocol", protocol);
            value.put("verified", verified);
            value.put("usable", usable);
            value.put("created_at", createdAt);
            value.put("recordings", recordings);
            if (fanLampProfile != null) {
                value.put("fanlamp_profile", fanLampProfile.toJson());
            }
            return value;
        }

        static RemoteProfile fromJson(JSONObject value) throws JSONException {
            FanLampRemoteProtocol.Profile protocolProfile = null;
            if (value.has("fanlamp_profile")) {
                protocolProfile = FanLampRemoteProtocol.Profile.fromJson(
                        value.getJSONObject("fanlamp_profile"));
            }
            return new RemoteProfile(
                    value.getString("id"),
                    value.getString("name"),
                    value.getString("protocol"),
                    value.optBoolean("verified", false),
                    value.optBoolean("usable", protocolProfile != null),
                    value.optLong("created_at", 0L),
                    protocolProfile,
                    value.optJSONObject("recordings") == null
                            ? new JSONObject() : value.getJSONObject("recordings"));
        }
    }
}
