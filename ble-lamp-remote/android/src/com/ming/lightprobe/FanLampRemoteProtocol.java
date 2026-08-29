package com.ming.lightprobe;

import java.security.SecureRandom;

import org.json.JSONException;
import org.json.JSONObject;

/** Encoder for the 0x5556 manufacturer advertisements emitted by this remote. */
public final class FanLampRemoteProtocol {
    public static final int MANUFACTURER_ID = 0x5556;
    public static final String PREFS_NAME = "fanlamp_remote";
    public static final String PREF_TX_COUNT = "phone_tx_count";
    public static final int TX_COUNT_MODULUS = 256;
    private static final byte[] PREFIX_AFTER_COMPANY = new byte[] {
            0x18, (byte) 0x87, 0x52, (byte) 0xB6, 0x5F, 0x2B,
            0x5E, 0x00, (byte) 0xFC, 0x31, 0x51
    };
    private static final SecureRandom RANDOM = new SecureRandom();
    private static final Profile BUILTIN_PROFILE = new Profile(
            PREFIX_AFTER_COMPANY, 0xE6, 0x10, 0x00, 0x01, 0x01, 0x93, 0x72);

    private FanLampRemoteProtocol() {
    }

    public static int randomSequenceByte() {
        return 1 + RANDOM.nextInt(255);
    }

    public static byte[] encode(int command, int parameter, int arg0, int arg1,
            int arg2, int txCount) {
        return encode(BUILTIN_PROFILE, command, parameter, arg0, arg1, arg2, txCount);
    }

    public static byte[] encode(Profile profile, int command, int parameter,
            int arg0, int arg1, int arg2, int txCount) {
        int seed = 1 + RANDOM.nextInt(0xFFF5);
        return encodeWithSeed(profile, command, parameter, arg0, arg1, arg2,
                txCount, seed);
    }

    static byte[] encodeWithSeed(int command, int parameter, int arg0, int arg1,
            int arg2, int txCount, int seed) {
        return encodeWithSeed(BUILTIN_PROFILE, command, parameter, arg0, arg1,
                arg2, txCount, seed);
    }

    static byte[] encodeWithSeed(Profile profile, int command, int parameter,
            int arg0, int arg1, int arg2, int txCount, int seed) {
        byte[] decoded = new byte[14];
        decoded[0] = (byte) command;
        decoded[1] = (byte) profile.idLow;
        decoded[2] = (byte) profile.idHigh;
        decoded[3] = (byte) arg0;
        decoded[4] = (byte) arg1;
        decoded[5] = (byte) (command == 0x22 ? arg2 : profile.defaultArg2);
        decoded[6] = (byte) txCount;
        decoded[7] = (byte) parameter;
        decoded[8] = (byte) ((seed & 0xFF) ^ profile.byte8Mask);
        decoded[9] = (byte) ((seed & 0xFF) ^ profile.byte9Mask);
        decoded[10] = (byte) (seed >>> 8);
        decoded[11] = (byte) seed;

        int crc = crc16(decoded, 0, 12, seed ^ 0xFFFF);
        decoded[12] = (byte) (crc >>> 8);
        decoded[13] = (byte) crc;

        byte[] readable = new byte[16];
        System.arraycopy(decoded, 0, readable, 0, decoded.length);
        readable[14] = (byte) profile.crc2High;
        readable[15] = (byte) profile.crc2Low;
        byte[] encrypted = whiten(reverseBits(readable), 0x0C);

        byte[] prefix = profile.prefixAfterCompany;
        byte[] manufacturerData = new byte[prefix.length + encrypted.length];
        System.arraycopy(prefix, 0, manufacturerData, 0, prefix.length);
        System.arraycopy(encrypted, 0, manufacturerData, prefix.length,
                encrypted.length);
        return manufacturerData;
    }

    /** Returns -1 when the packet is not a valid packet from the captured remote. */
    public static int decodeCounter(byte[] manufacturerData) {
        if (manufacturerData == null
                || manufacturerData.length != PREFIX_AFTER_COMPANY.length + 16) {
            return -1;
        }
        for (int i = 0; i < PREFIX_AFTER_COMPANY.length; i++) {
            if (manufacturerData[i] != PREFIX_AFTER_COMPANY[i]) {
                return -1;
            }
        }
        DecodedFrame frame = decodeAny(manufacturerData);
        return frame == null ? -1 : frame.txCount;
    }

    /** Decodes any compatible 27-byte FanLamp V1 payload, without fixing its profile. */
    public static DecodedFrame decodeAny(byte[] manufacturerData) {
        if (manufacturerData == null || manufacturerData.length != 27) {
            return null;
        }
        int prefixLength = manufacturerData.length - 16;
        byte[] encrypted = new byte[16];
        System.arraycopy(manufacturerData, prefixLength,
                encrypted, 0, encrypted.length);
        byte[] decoded = reverseBits(whiten(encrypted, 0x0C));
        int seed = ((decoded[10] & 0xFF) << 8) | (decoded[11] & 0xFF);
        if (seed <= 0 || seed > 0xFFF5) {
            return null;
        }
        int expectedCrc = crc16(decoded, 0, 12, seed ^ 0xFFFF);
        int packetCrc = ((decoded[12] & 0xFF) << 8) | (decoded[13] & 0xFF);
        if (expectedCrc != packetCrc) {
            return null;
        }
        byte[] prefix = new byte[prefixLength];
        System.arraycopy(manufacturerData, 0, prefix, 0, prefixLength);
        Profile profile = new Profile(
                prefix,
                decoded[1] & 0xFF,
                decoded[2] & 0xFF,
                decoded[5] & 0xFF,
                (seed & 0xFF) ^ (decoded[8] & 0xFF),
                (seed & 0xFF) ^ (decoded[9] & 0xFF),
                decoded[14] & 0xFF,
                decoded[15] & 0xFF);
        return new DecodedFrame(
                decoded[0] & 0xFF,
                decoded[7] & 0xFF,
                decoded[3] & 0xFF,
                decoded[4] & 0xFF,
                decoded[5] & 0xFF,
                decoded[6] & 0xFF,
                profile);
    }

    public static Profile builtinProfile() {
        return BUILTIN_PROFILE;
    }

    public static String toHex(byte[] bytes) {
        char[] digits = "0123456789ABCDEF".toCharArray();
        char[] output = new char[bytes.length * 2];
        for (int i = 0; i < bytes.length; i++) {
            int value = bytes[i] & 0xFF;
            output[i * 2] = digits[value >>> 4];
            output[i * 2 + 1] = digits[value & 0x0F];
        }
        return new String(output);
    }

    private static byte[] fromHex(String value) {
        if (value == null || value.length() % 2 != 0) {
            throw new IllegalArgumentException("Invalid hexadecimal value");
        }
        byte[] output = new byte[value.length() / 2];
        for (int i = 0; i < output.length; i++) {
            int high = Character.digit(value.charAt(i * 2), 16);
            int low = Character.digit(value.charAt(i * 2 + 1), 16);
            if (high < 0 || low < 0) {
                throw new IllegalArgumentException("Invalid hexadecimal value");
            }
            output[i] = (byte) ((high << 4) | low);
        }
        return output;
    }

    public static final class Profile {
        private final byte[] prefixAfterCompany;
        private final int idLow;
        private final int idHigh;
        private final int defaultArg2;
        private final int byte8Mask;
        private final int byte9Mask;
        private final int crc2High;
        private final int crc2Low;

        Profile(byte[] prefixAfterCompany, int idLow, int idHigh,
                int defaultArg2, int byte8Mask, int byte9Mask,
                int crc2High, int crc2Low) {
            if (prefixAfterCompany == null || prefixAfterCompany.length != 11) {
                throw new IllegalArgumentException("FanLamp V1 prefix must be 11 bytes");
            }
            this.prefixAfterCompany = prefixAfterCompany.clone();
            this.idLow = idLow & 0xFF;
            this.idHigh = idHigh & 0xFF;
            this.defaultArg2 = defaultArg2 & 0xFF;
            this.byte8Mask = byte8Mask & 0xFF;
            this.byte9Mask = byte9Mask & 0xFF;
            this.crc2High = crc2High & 0xFF;
            this.crc2Low = crc2Low & 0xFF;
        }

        public JSONObject toJson() throws JSONException {
            JSONObject value = new JSONObject();
            value.put("prefix", toHex(prefixAfterCompany));
            value.put("id_low", idLow);
            value.put("id_high", idHigh);
            value.put("default_arg2", defaultArg2);
            value.put("byte8_mask", byte8Mask);
            value.put("byte9_mask", byte9Mask);
            value.put("crc2_high", crc2High);
            value.put("crc2_low", crc2Low);
            return value;
        }

        public static Profile fromJson(JSONObject value) throws JSONException {
            return new Profile(
                    fromHex(value.getString("prefix")),
                    value.getInt("id_low"),
                    value.getInt("id_high"),
                    value.getInt("default_arg2"),
                    value.getInt("byte8_mask"),
                    value.getInt("byte9_mask"),
                    value.getInt("crc2_high"),
                    value.getInt("crc2_low"));
        }

        public String fingerprint() {
            return toHex(prefixAfterCompany) + ':' + idLow + ':' + idHigh + ':'
                    + defaultArg2 + ':' + byte8Mask + ':' + byte9Mask + ':'
                    + crc2High + ':' + crc2Low;
        }
    }

    public static final class DecodedFrame {
        public final int command;
        public final int parameter;
        public final int arg0;
        public final int arg1;
        public final int arg2;
        public final int txCount;
        public final Profile profile;

        DecodedFrame(int command, int parameter, int arg0, int arg1,
                int arg2, int txCount, Profile profile) {
            this.command = command;
            this.parameter = parameter;
            this.arg0 = arg0;
            this.arg1 = arg1;
            this.arg2 = arg2;
            this.txCount = txCount;
            this.profile = profile;
        }

        public String normalizedSignature() {
            return command + ":" + parameter + ":" + arg0 + ':' + arg1 + ':' + arg2;
        }

        public String actionKey() {
            if (command == 0x10) {
                return LightCommandDispatcher.ACTION_ON;
            }
            if (command == 0x11) {
                return LightCommandDispatcher.ACTION_OFF;
            }
            if (command == 0x39) {
                if (parameter == 0) return LightCommandDispatcher.ACTION_BRIGHTER;
                if (parameter == 1) return LightCommandDispatcher.ACTION_DIMMER;
                if (parameter == 2) return LightCommandDispatcher.ACTION_COOLER;
                if (parameter == 3) return LightCommandDispatcher.ACTION_WARMER;
            }
            if (command == 0x21) {
                if (parameter == 0x14) return LightCommandDispatcher.ACTION_BRIGHTER;
                if (parameter == 0x28) return LightCommandDispatcher.ACTION_DIMMER;
                if (parameter == 0x24) return LightCommandDispatcher.ACTION_COOLER;
                if (parameter == 0x18) return LightCommandDispatcher.ACTION_WARMER;
                if (parameter == 0x02) return "preset_full";
                if (parameter == 0x01) return "preset_half";
            }
            return null;
        }
    }

    private static int crc16(byte[] data, int offset, int length, int seed) {
        int crc = seed & 0xFFFF;
        for (int i = offset; i < offset + length; i++) {
            crc ^= (data[i] & 0xFF) << 8;
            for (int bit = 0; bit < 8; bit++) {
                crc = (crc & 0x8000) != 0
                        ? ((crc << 1) ^ 0x1021) & 0xFFFF
                        : (crc << 1) & 0xFFFF;
            }
        }
        return crc;
    }

    private static byte[] reverseBits(byte[] input) {
        byte[] output = new byte[input.length];
        for (int i = 0; i < input.length; i++) {
            int value = input[i] & 0xFF;
            value = ((value & 0x55) << 1) | ((value & 0xAA) >>> 1);
            value = ((value & 0x33) << 2) | ((value & 0xCC) >>> 2);
            value = ((value & 0x0F) << 4) | ((value & 0xF0) >>> 4);
            output[i] = (byte) value;
        }
        return output;
    }

    private static byte[] whiten(byte[] input, int seed) {
        byte[] output = new byte[input.length];
        int register = seed;
        for (int i = 0; i < input.length; i++) {
            int mask = 0;
            for (int bit = 0; bit < 8; bit++) {
                register <<= 1;
                if ((register & 0x80) != 0) {
                    register ^= 0x11;
                    mask |= 1 << bit;
                }
                register &= 0x7F;
            }
            output[i] = (byte) ((input[i] & 0xFF) ^ mask);
        }
        return output;
    }
}
