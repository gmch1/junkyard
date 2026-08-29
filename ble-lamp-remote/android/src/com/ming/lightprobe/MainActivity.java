package com.ming.lightprobe;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Canvas;
import android.graphics.Color;
import android.graphics.Paint;
import android.graphics.RectF;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.StateListDrawable;
import android.os.Build;
import android.os.Bundle;
import android.util.Log;
import android.view.Gravity;
import android.view.HapticFeedbackConstants;
import android.view.View;
import android.widget.FrameLayout;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import java.util.ArrayList;
import java.util.List;

import org.json.JSONException;
import org.json.JSONObject;

public final class MainActivity extends Activity {
    private static final int PERMISSION_REQUEST = 100;
    private static final int DEFAULT_MIN_RSSI = -75;
    private static final String SCREEN_HOME = "home";
    private static final String SCREEN_CONTROL = "control";
    private static final String SCREEN_SETTINGS = "settings";
    private static final String SCREEN_LEARNING = "learning";

    private TextView statusView;
    private TextView learningStepView;
    private TextView learningTitleView;
    private TextView learningInstructionView;
    private TextView learningMessageView;
    private TextView learningCountView;
    private TextView updateStatusView;
    private final View[] learningDots = new View[3];
    private RemoteLearningController learningController;
    private AppUpdateManager appUpdateManager;
    private String currentScreen = SCREEN_HOME;
    private int scanMinRssi = DEFAULT_MIN_RSSI;
    private boolean pendingStart;
    private boolean pendingApiStart;
    private boolean pendingLearningStart;
    private Intent pendingAdvertiseIntent;
    private int[][] pendingRemoteCommands;
    private String pendingRemoteLabel;
    private String pendingNamedRemoteAction;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        appUpdateManager = new AppUpdateManager(this, new AppUpdateManager.Listener() {
            @Override
            public void onStatusChanged(String message, boolean busy) {
                if (updateStatusView != null) {
                    updateStatusView.setText(message);
                }
            }

            @Override
            public void onUpdateAvailable(AppUpdateManager.Release release) {
                showUpdateAvailable(release);
            }
        });
        showControlScreen();
        if (LightApiService.isEnabled(this)) {
            requestApiStart();
        }
        handleIntent(getIntent());
        appUpdateManager.checkForUpdates(false);
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (appUpdateManager != null) {
            appUpdateManager.resumeAfterInstallPermission();
        }
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        handleIntent(intent);
    }

    private void showControlScreen() {
        stopLearning();
        currentScreen = SCREEN_CONTROL;
        configureWindowColors();

        int edge = dp(22);

        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        scroll.setVerticalScrollBarEnabled(false);
        GradientDrawable pageBackground = new GradientDrawable(
                GradientDrawable.Orientation.TOP_BOTTOM,
                new int[] {Color.rgb(255, 240, 188), Color.rgb(255, 251, 236)});
        scroll.setBackground(pageBackground);

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setGravity(Gravity.CENTER_HORIZONTAL);
        root.setPadding(edge, dp(8), edge, dp(18));

        FrameLayout header = new FrameLayout(this);
        LinearLayout heading = new LinearLayout(this);
        heading.setOrientation(LinearLayout.VERTICAL);
        heading.setGravity(Gravity.CENTER);
        final RemoteProfileStore.RemoteProfile activeProfile =
                RemoteProfileStore.active(this);
        TextView title = centeredLabel(activeProfile.name, Color.rgb(29, 27, 23), 21);
        title.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        heading.addView(title, matchWrap());
        FrameLayout.LayoutParams headingParams = new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.WRAP_CONTENT,
                Gravity.CENTER);
        header.addView(heading, headingParams);

        TextView back = centeredLabel("‹", Color.rgb(50, 46, 39), 38);
        back.setContentDescription("返回");
        back.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                showHomeScreen();
            }
        });
        FrameLayout.LayoutParams backParams = new FrameLayout.LayoutParams(
                dp(46), dp(58), Gravity.START | Gravity.CENTER_VERTICAL);
        header.addView(back, backParams);

        TextView settings = centeredLabel("⚙", Color.rgb(100, 73, 28), 20);
        settings.setContentDescription("设置");
        settings.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                showSettingsScreen();
            }
        });
        FrameLayout.LayoutParams settingsParams = new FrameLayout.LayoutParams(
                dp(42), dp(58), Gravity.END | Gravity.CENTER_VERTICAL);
        settingsParams.rightMargin = dp(54);
        header.addView(settings, settingsParams);

        TextView learn = centeredLabel("录制", Color.rgb(100, 73, 28), 14);
        learn.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        learn.setContentDescription("录制新遥控器");
        learn.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                requestLearningStart();
            }
        });
        FrameLayout.LayoutParams learnParams = new FrameLayout.LayoutParams(
                dp(54), dp(58), Gravity.END | Gravity.CENTER_VERTICAL);
        header.addView(learn, learnParams);
        root.addView(header, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dp(60)));

        LinearLayout controls = new LinearLayout(this);
        controls.setOrientation(LinearLayout.VERTICAL);
        controls.setGravity(Gravity.CENTER);
        addRemoteControls(controls);
        root.addView(controls, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, 0, 1));

        // Keep a non-visible status sink for shared permission/error paths. The control
        // screen has no persistent or selectable status chrome.
        statusView = new TextView(this);

        scroll.addView(root, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT));
        setContentView(scroll);
    }

    private void showSettingsScreen() {
        stopLearning();
        currentScreen = SCREEN_SETTINGS;
        configureWindowColors();

        ScrollView scroll = pageScroll();
        LinearLayout root = pageRoot(22, 14, 28);

        FrameLayout header = new FrameLayout(this);
        TextView title = centeredLabel("设置", Color.rgb(31, 29, 25), 20);
        title.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        header.addView(title, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT, Gravity.CENTER));
        TextView back = centeredLabel("‹", Color.rgb(50, 46, 39), 38);
        back.setContentDescription("返回遥控器");
        back.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                showControlScreen();
            }
        });
        header.addView(back, new FrameLayout.LayoutParams(dp(46), dp(58),
                Gravity.START | Gravity.CENTER_VERTICAL));
        root.addView(header, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dp(58)));

        final boolean apiEnabled = LightApiService.isEnabled(this);
        LinearLayout apiCard = card();
        apiCard.addView(cardHeader("局域网 API", apiEnabled ? "已启用" : "默认关闭"));
        LinearLayout toggle = apiToggleButton(apiEnabled);
        LinearLayout.LayoutParams toggleParams = matchWrap();
        toggleParams.height = dp(56);
        toggleParams.setMargins(0, dp(16), 0, 0);
        apiCard.addView(toggle, toggleParams);

        TextView endpoint = new TextView(this);
        endpoint.setText(apiEnabled ? LightApiService.endpoint() : "启用后显示局域网地址");
        endpoint.setTextColor(Color.rgb(84, 78, 67));
        endpoint.setTextSize(13);
        endpoint.setTextIsSelectable(true);
        endpoint.setGravity(Gravity.CENTER);
        endpoint.setPadding(dp(8), dp(14), dp(8), dp(12));
        apiCard.addView(endpoint, matchWrap());

        if (apiEnabled) {
            LinearLayout apiActions = new LinearLayout(this);
            apiActions.setOrientation(LinearLayout.HORIZONTAL);
            LinearLayout copyAddress = copyButton("复制地址", LightApiService.endpoint());
            LinearLayout copyToken = copyButton("复制 Token",
                    LightApiService.getOrCreateToken(this));
            addPairButtons(apiActions, copyAddress, copyToken);
            apiCard.addView(apiActions, matchWrap());
        }
        addCard(root, apiCard);

        LinearLayout updateCard = card();
        updateCard.addView(cardHeader("应用更新",
                "v" + appUpdateManager.currentVersionName()));
        updateStatusView = centeredLabel(appUpdateManager.currentStatus(),
                Color.rgb(84, 78, 67), 13);
        updateStatusView.setPadding(dp(8), dp(14), dp(8), dp(12));
        updateCard.addView(updateStatusView, matchWrap());
        LinearLayout checkUpdate = updateCheckButton();
        LinearLayout.LayoutParams checkParams = matchWrap();
        checkParams.height = dp(56);
        updateCard.addView(checkUpdate, checkParams);
        addCard(root, updateCard);

        TextView note = centeredLabel(
                "Token 仅用于可信局域网内的 Mac 遥控，请勿公开分享",
                Color.rgb(112, 99, 72), 13);
        note.setPadding(dp(16), dp(12), dp(16), dp(12));
        LinearLayout.LayoutParams noteParams = matchWrap();
        noteParams.setMargins(0, dp(12), 0, 0);
        root.addView(note, noteParams);

        statusView = centeredLabel(apiEnabled
                        ? "●  局域网遥控服务已启动"
                        : "●  局域网遥控服务已关闭",
                Color.rgb(112, 99, 72), 13);
        statusView.setTextIsSelectable(true);
        statusView.setPadding(dp(16), dp(12), dp(16), dp(12));
        statusView.setBackground(rounded(Color.argb(120, 255, 255, 255), 22));
        LinearLayout.LayoutParams statusParams = matchWrap();
        statusParams.setMargins(0, dp(8), 0, 0);
        root.addView(statusView, statusParams);

        scroll.addView(root);
        setContentView(scroll);
    }

    private void showHomeScreen() {
        stopLearning();
        currentScreen = SCREEN_HOME;
        configureWindowColors();

        ScrollView scroll = pageScroll();
        LinearLayout root = pageRoot(22, 22, 30);

        TextView eyebrow = new TextView(this);
        eyebrow.setText("BLE REMOTE");
        eyebrow.setTextColor(Color.rgb(164, 112, 35));
        eyebrow.setTextSize(12);
        eyebrow.setLetterSpacing(0.12f);
        eyebrow.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        root.addView(eyebrow, matchWrap());

        TextView title = new TextView(this);
        title.setText("我的遥控器");
        title.setTextColor(Color.rgb(29, 27, 23));
        title.setTextSize(30);
        title.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        LinearLayout.LayoutParams titleParams = matchWrap();
        titleParams.setMargins(0, dp(5), 0, 0);
        root.addView(title, titleParams);

        TextView intro = new TextView(this);
        intro.setText("选择已适配设备，或录制一只新的遥控器");
        intro.setTextColor(Color.rgb(111, 101, 82));
        intro.setTextSize(15);
        LinearLayout.LayoutParams introParams = matchWrap();
        introParams.setMargins(0, dp(5), 0, dp(25));
        root.addView(intro, introParams);

        root.addView(sectionLabel("已验证设备"), matchWrap());
        for (final RemoteProfileStore.RemoteProfile profile : RemoteProfileStore.list(this)) {
            LinearLayout profileCard = deviceCard(profile);
            profileCard.setOnClickListener(new View.OnClickListener() {
                @Override
                public void onClick(View view) {
                    if (!profile.usable) {
                        Toast.makeText(MainActivity.this,
                                "样本已保存，当前协议还不能直接发送",
                                Toast.LENGTH_SHORT).show();
                        return;
                    }
                    RemoteProfileStore.activate(MainActivity.this, profile.id);
                    showControlScreen();
                }
            });
            addCard(root, profileCard);
        }

        LinearLayout recordButton = new LinearLayout(this);
        recordButton.setOrientation(LinearLayout.HORIZONTAL);
        recordButton.setGravity(Gravity.CENTER_VERTICAL);
        recordButton.setPadding(dp(19), dp(16), dp(19), dp(16));
        recordButton.setBackground(touchBackground(
                Color.rgb(43, 40, 34), Color.rgb(69, 62, 50), 20));
        recordButton.setClickable(true);
        recordButton.setFocusable(true);
        TextView plus = centeredLabel("＋", Color.rgb(255, 210, 103), 28);
        recordButton.addView(plus, new LinearLayout.LayoutParams(dp(40), dp(40)));
        LinearLayout recordText = new LinearLayout(this);
        recordText.setOrientation(LinearLayout.VERTICAL);
        TextView recordTitle = new TextView(this);
        recordTitle.setText("录制新遥控器");
        recordTitle.setTextColor(Color.WHITE);
        recordTitle.setTextSize(17);
        recordTitle.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        recordText.addView(recordTitle, matchWrap());
        TextView recordHelp = new TextView(this);
        recordHelp.setText("按提示操作，识别三次后自动继续");
        recordHelp.setTextColor(Color.rgb(205, 198, 184));
        recordHelp.setTextSize(13);
        recordText.addView(recordHelp, matchWrap());
        LinearLayout.LayoutParams recordTextParams = new LinearLayout.LayoutParams(
                0, LinearLayout.LayoutParams.WRAP_CONTENT, 1);
        recordTextParams.setMargins(dp(12), 0, 0, 0);
        recordButton.addView(recordText, recordTextParams);
        TextView arrow = centeredLabel("›", Color.WHITE, 30);
        recordButton.addView(arrow, new LinearLayout.LayoutParams(dp(28), dp(40)));
        recordButton.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                requestLearningStart();
            }
        });
        LinearLayout.LayoutParams recordParams = matchWrap();
        recordParams.setMargins(0, dp(18), 0, 0);
        root.addView(recordButton, recordParams);

        statusView = centeredLabel(LightApiService.isEnabled(this)
                        ? "●  局域网遥控服务准备中"
                        : "●  局域网遥控服务已关闭",
                Color.rgb(112, 99, 72), 13);
        statusView.setTextIsSelectable(true);
        statusView.setPadding(dp(14), dp(11), dp(14), dp(11));
        statusView.setBackground(rounded(Color.argb(115, 255, 255, 255), 18));
        LinearLayout.LayoutParams statusParams = matchWrap();
        statusParams.setMargins(0, dp(18), 0, 0);
        root.addView(statusView, statusParams);

        scroll.addView(root);
        setContentView(scroll);
    }

    private LinearLayout deviceCard(RemoteProfileStore.RemoteProfile profile) {
        LinearLayout container = card();
        container.setOrientation(LinearLayout.HORIZONTAL);
        container.setGravity(Gravity.CENTER_VERTICAL);
        container.setClickable(true);
        container.setFocusable(true);
        container.setBackground(touchBackground(Color.WHITE,
                Color.rgb(255, 249, 229), 20));

        FrameLayout badge = new FrameLayout(this);
        badge.setBackground(rounded(
                profile.usable ? Color.rgb(255, 236, 174) : Color.rgb(235, 233, 228), 15));
        TextView bulb = centeredLabel(profile.usable ? "☀" : "●",
                profile.usable ? Color.rgb(188, 121, 20) : Color.rgb(132, 127, 117), 24);
        badge.addView(bulb, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT));
        container.addView(badge, new LinearLayout.LayoutParams(dp(54), dp(54)));

        LinearLayout words = new LinearLayout(this);
        words.setOrientation(LinearLayout.VERTICAL);
        TextView name = new TextView(this);
        name.setText(profile.name);
        name.setTextColor(Color.rgb(31, 29, 25));
        name.setTextSize(17);
        name.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        words.addView(name, matchWrap());
        TextView details = new TextView(this);
        String detail = profile.verified
                ? "BLE · 已实机验证"
                : (profile.usable ? "BLE · 已完成录制" : "样本已保存 · 待适配");
        details.setText(detail);
        details.setTextColor(Color.rgb(126, 117, 100));
        details.setTextSize(13);
        words.addView(details, matchWrap());
        LinearLayout.LayoutParams wordsParams = new LinearLayout.LayoutParams(
                0, LinearLayout.LayoutParams.WRAP_CONTENT, 1);
        wordsParams.setMargins(dp(15), 0, 0, 0);
        container.addView(words, wordsParams);

        TextView state = centeredLabel(profile.verified ? "已验证" : "›",
                profile.verified ? Color.rgb(151, 96, 17) : Color.rgb(103, 96, 82),
                profile.verified ? 12 : 28);
        if (profile.verified) {
            state.setPadding(dp(10), dp(5), dp(10), dp(5));
            state.setBackground(rounded(Color.rgb(255, 244, 208), 12));
        }
        container.addView(state, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT, dp(34)));
        return container;
    }

    private TextView sectionLabel(String text) {
        TextView label = new TextView(this);
        label.setText(text);
        label.setTextColor(Color.rgb(86, 79, 66));
        label.setTextSize(14);
        label.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        return label;
    }

    private ScrollView pageScroll() {
        ScrollView scroll = new ScrollView(this);
        scroll.setFillViewport(true);
        scroll.setVerticalScrollBarEnabled(false);
        scroll.setBackground(new GradientDrawable(
                GradientDrawable.Orientation.TOP_BOTTOM,
                new int[] {Color.rgb(255, 240, 188), Color.rgb(255, 251, 236)}));
        return scroll;
    }

    private LinearLayout pageRoot(int horizontal, int top, int bottom) {
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(horizontal), dp(top), dp(horizontal), dp(bottom));
        return root;
    }

    private void requestLearningStart() {
        List<String> missing = missingPermissions();
        if (!missing.isEmpty()) {
            pendingLearningStart = true;
            statusView.setText("请授权附近设备权限，以识别遥控器按键");
            requestPermissions(missing.toArray(new String[0]), PERMISSION_REQUEST);
            return;
        }
        pendingLearningStart = false;
        showLearningScreen();
    }

    private void showLearningScreen() {
        stopLearning();
        currentScreen = SCREEN_LEARNING;
        configureWindowColors();

        ScrollView scroll = pageScroll();
        final LinearLayout root = pageRoot(22, 14, 28);

        FrameLayout header = new FrameLayout(this);
        TextView headerTitle = centeredLabel("录制遥控器", Color.rgb(31, 29, 25), 19);
        headerTitle.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        header.addView(headerTitle, new FrameLayout.LayoutParams(
                FrameLayout.LayoutParams.MATCH_PARENT,
                FrameLayout.LayoutParams.MATCH_PARENT, Gravity.CENTER));
        TextView back = centeredLabel("‹", Color.rgb(50, 46, 39), 38);
        back.setContentDescription("取消录制");
        back.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                showHomeScreen();
            }
        });
        header.addView(back, new FrameLayout.LayoutParams(dp(46), dp(58),
                Gravity.START | Gravity.CENTER_VERTICAL));
        root.addView(header, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dp(58)));

        learningStepView = centeredLabel("准备扫描", Color.rgb(151, 96, 17), 13);
        learningStepView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        LinearLayout.LayoutParams stepParams = matchWrap();
        stepParams.setMargins(0, dp(26), 0, 0);
        root.addView(learningStepView, stepParams);

        learningTitleView = centeredLabel("先不要按遥控器", Color.rgb(28, 26, 23), 29);
        learningTitleView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        LinearLayout.LayoutParams learningTitleParams = matchWrap();
        learningTitleParams.setMargins(0, dp(10), 0, 0);
        root.addView(learningTitleView, learningTitleParams);

        learningInstructionView = centeredLabel(
                "正在识别周围持续广播的设备", Color.rgb(111, 100, 81), 15);
        LinearLayout.LayoutParams instructionParams = matchWrap();
        instructionParams.setMargins(0, dp(8), 0, dp(26));
        root.addView(learningInstructionView, instructionParams);

        LinearLayout instructionCard = card();
        instructionCard.setGravity(Gravity.CENTER_HORIZONTAL);
        instructionCard.setPadding(dp(22), dp(30), dp(22), dp(28));
        TextView remoteGlyph = centeredLabel("⌁", Color.rgb(181, 116, 22), 42);
        remoteGlyph.setBackground(rounded(Color.rgb(255, 241, 195), 28));
        instructionCard.addView(remoteGlyph,
                new LinearLayout.LayoutParams(dp(66), dp(66)));

        learningMessageView = centeredLabel(
                "校准环境中…", Color.rgb(67, 61, 51), 16);
        learningMessageView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        LinearLayout.LayoutParams messageParams = matchWrap();
        messageParams.setMargins(0, dp(19), 0, 0);
        instructionCard.addView(learningMessageView, messageParams);

        LinearLayout dots = new LinearLayout(this);
        dots.setGravity(Gravity.CENTER);
        for (int i = 0; i < learningDots.length; i++) {
            View dot = new View(this);
            dot.setBackground(rounded(Color.rgb(222, 218, 207), 7));
            LinearLayout.LayoutParams dotParams = new LinearLayout.LayoutParams(
                    dp(13), dp(13));
            dotParams.setMargins(dp(5), 0, dp(5), 0);
            dots.addView(dot, dotParams);
            learningDots[i] = dot;
        }
        LinearLayout.LayoutParams dotsParams = matchWrap();
        dotsParams.setMargins(0, dp(24), 0, 0);
        instructionCard.addView(dots, dotsParams);

        learningCountView = centeredLabel("已识别 0/3", Color.rgb(126, 117, 100), 13);
        LinearLayout.LayoutParams countParams = matchWrap();
        countParams.setMargins(0, dp(10), 0, 0);
        instructionCard.addView(learningCountView, countParams);
        root.addView(instructionCard, matchWrap());

        TextView automatic = centeredLabel(
                "每次短按一次，然后松开并等待约 1 秒\n识别到 3 次后会自动进入下一项",
                Color.rgb(118, 107, 88), 14);
        automatic.setLineSpacing(dp(3), 1f);
        LinearLayout.LayoutParams automaticParams = matchWrap();
        automaticParams.setMargins(0, dp(20), 0, 0);
        root.addView(automatic, automaticParams);

        TextView skip = centeredLabel("这个遥控器没有此按键，跳过", Color.rgb(130, 103, 60), 14);
        skip.setPadding(dp(10), dp(16), dp(10), dp(16));
        skip.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                if (learningController != null) {
                    learningController.skipCurrentStep();
                }
            }
        });
        LinearLayout.LayoutParams skipParams = matchWrap();
        skipParams.setMargins(0, dp(10), 0, 0);
        root.addView(skip, skipParams);

        statusView = centeredLabel("正在启动蓝牙扫描", Color.rgb(112, 99, 72), 12);
        LinearLayout.LayoutParams statusParams = matchWrap();
        statusParams.setMargins(0, dp(8), 0, 0);
        root.addView(statusView, statusParams);

        scroll.addView(root);
        setContentView(scroll);

        learningController = new RemoteLearningController(this,
                new RemoteLearningController.Listener() {
            @Override
            public void onCalibrating(int secondsRemaining) {
                if (!SCREEN_LEARNING.equals(currentScreen)) return;
                learningStepView.setText("环境校准");
                learningTitleView.setText("先不要按遥控器");
                learningInstructionView.setText("正在排除其他蓝牙设备");
                learningMessageView.setText("约 " + secondsRemaining + " 秒后开始");
                learningCountView.setText("准备中");
                updateLearningDots(0);
            }

            @Override
            public void onStepChanged(int index, int total,
                    RemoteLearningController.Step step, int acceptedPresses,
                    String message) {
                if (!SCREEN_LEARNING.equals(currentScreen)) return;
                learningStepView.setText("步骤 " + (index + 1) + " / " + total);
                learningTitleView.setText(step.title);
                learningInstructionView.setText(step.instruction);
                learningMessageView.setText(message);
                learningCountView.setText("已识别 " + acceptedPresses + "/3");
                updateLearningDots(acceptedPresses);
                statusView.setText("正在监听遥控器广播");
            }

            @Override
            public void onCompleted(RemoteLearningController.Result result) {
                if (!SCREEN_LEARNING.equals(currentScreen)) return;
                RemoteProfileStore.RemoteProfile profile =
                        RemoteProfileStore.saveLearned(MainActivity.this,
                                result.fanLampProfile, result.recordings);
                showLearningComplete(profile, result.protocolName);
            }

            @Override
            public void onError(String message) {
                if (!SCREEN_LEARNING.equals(currentScreen)) return;
                learningMessageView.setText(message);
                statusView.setText("录制已停止");
                Toast.makeText(MainActivity.this, message, Toast.LENGTH_LONG).show();
            }
        });
        learningController.start();
    }

    private void updateLearningDots(int accepted) {
        for (int i = 0; i < learningDots.length; i++) {
            if (learningDots[i] != null) {
                learningDots[i].setBackground(rounded(
                        i < accepted ? Color.rgb(225, 151, 34)
                                : Color.rgb(222, 218, 207), 7));
            }
        }
    }

    private void showLearningComplete(final RemoteProfileStore.RemoteProfile profile,
            String protocolName) {
        stopLearning();
        currentScreen = SCREEN_LEARNING;
        configureWindowColors();
        LinearLayout root = pageRoot(24, 46, 34);
        root.setGravity(Gravity.CENTER_HORIZONTAL);
        root.setBackground(new GradientDrawable(
                GradientDrawable.Orientation.TOP_BOTTOM,
                new int[] {Color.rgb(255, 240, 188), Color.rgb(255, 251, 236)}));

        TextView check = centeredLabel(profile.usable ? "✓" : "●",
                profile.usable ? Color.rgb(107, 143, 76) : Color.rgb(171, 120, 43), 38);
        check.setBackground(rounded(Color.WHITE, 36));
        root.addView(check, new LinearLayout.LayoutParams(dp(72), dp(72)));
        TextView title = centeredLabel("录制完成", Color.rgb(29, 27, 23), 29);
        title.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        LinearLayout.LayoutParams titleParams = matchWrap();
        titleParams.setMargins(0, dp(22), 0, 0);
        root.addView(title, titleParams);
        TextView details = centeredLabel(
                profile.usable
                        ? "已识别 " + protocolName + "\n现在可以直接控制这台灯"
                        : "七组按键样本已保存在手机上\n当前协议还需要适配后才能发送",
                Color.rgb(105, 95, 78), 15);
        details.setLineSpacing(dp(4), 1f);
        LinearLayout.LayoutParams detailParams = matchWrap();
        detailParams.setMargins(0, dp(10), 0, dp(30));
        root.addView(details, detailParams);

        TextView primary = centeredLabel(profile.usable ? "立即使用" : "返回设备列表",
                Color.WHITE, 16);
        primary.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        primary.setBackground(touchBackground(Color.rgb(43, 40, 34),
                Color.rgb(69, 62, 50), 18));
        primary.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                if (profile.usable) {
                    RemoteProfileStore.activate(MainActivity.this, profile.id);
                    showControlScreen();
                } else {
                    showHomeScreen();
                }
            }
        });
        root.addView(primary, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dp(56)));

        if (profile.usable) {
            TextView secondary = centeredLabel("返回设备列表", Color.rgb(115, 91, 53), 14);
            secondary.setPadding(dp(12), dp(18), dp(12), dp(18));
            secondary.setOnClickListener(new View.OnClickListener() {
                @Override
                public void onClick(View view) {
                    showHomeScreen();
                }
            });
            root.addView(secondary, matchWrap());
        }
        statusView = centeredLabel("录制数据仅保存在本机", Color.rgb(131, 121, 104), 12);
        LinearLayout.LayoutParams statusParams = matchWrap();
        statusParams.setMargins(0, dp(18), 0, 0);
        root.addView(statusView, statusParams);
        setContentView(root);
    }

    private void stopLearning() {
        if (learningController != null) {
            learningController.stop();
            learningController = null;
        }
    }

    private void addRemoteControls(LinearLayout root) {
        LinearLayout powerRow = new LinearLayout(this);
        powerRow.setOrientation(LinearLayout.HORIZONTAL);

        LinearLayout powerOn = powerButton("开启", true, "开灯",
                new int[][] {{0x10, 0, 0, 0, 0}});
        LinearLayout powerOff = powerButton("关闭", false, "关灯",
                new int[][] {{0x11, 0, 0, 0, 0}});

        LinearLayout.LayoutParams powerButtonParams = new LinearLayout.LayoutParams(
                0, dp(72), 1);
        powerButtonParams.setMargins(0, 0, dp(6), 0);
        powerRow.addView(powerOn, powerButtonParams);
        LinearLayout.LayoutParams offParams = new LinearLayout.LayoutParams(
                0, dp(72), 1);
        offParams.setMargins(dp(6), 0, 0, 0);
        powerRow.addView(powerOff, offParams);
        root.addView(powerRow, matchWrap());

        LinearLayout brightnessRow = new LinearLayout(this);
        brightnessRow.setOrientation(LinearLayout.HORIZONTAL);
        LinearLayout dimmer = sceneButton("调暗", RemoteIconView.MINUS, "亮度降低",
                new int[][] {{0x39, 1, 0, 0, 0}, {0x21, 0x28, 0, 2, 0}},
                Color.rgb(249, 246, 237), Color.rgb(76, 69, 55));
        LinearLayout brighter = sceneButton("调亮", RemoteIconView.PLUS, "亮度增加",
                new int[][] {{0x39, 0, 0, 0, 0}, {0x21, 0x14, 0, 2, 0}},
                Color.rgb(255, 241, 204), Color.rgb(119, 79, 19));
        addRemotePairButtons(brightnessRow, dimmer, brighter);
        LinearLayout.LayoutParams brightnessParams = matchWrap();
        brightnessParams.setMargins(0, dp(12), 0, 0);
        root.addView(brightnessRow, brightnessParams);

        LinearLayout toggleRow = new LinearLayout(this);
        toggleRow.setOrientation(LinearLayout.HORIZONTAL);
        LinearLayout temperature = sceneButton("冷光 / 暖光", RemoteIconView.WARM,
                "切换暖光", warmCommands(),
                Color.rgb(255, 239, 218), Color.rgb(133, 75, 30));
        setAlternatingRemoteAction(temperature,
                "切换暖光", warmCommands(), "切换冷光", coolCommands());
        LinearLayout brightnessPreset = sceneButton("全亮 / 半亮", RemoteIconView.HALF,
                "切换半亮", new int[][] {{0x21, 1, 127, 127, 0}},
                Color.rgb(241, 239, 248), Color.rgb(76, 69, 105));
        setAlternatingRemoteAction(brightnessPreset,
                "切换半亮", new int[][] {{0x21, 1, 127, 127, 0}},
                "切换全亮", new int[][] {{0x21, 2, 255, 255, 0}});
        addRemotePairButtons(toggleRow, temperature, brightnessPreset);
        LinearLayout.LayoutParams toggleParams = matchWrap();
        toggleParams.setMargins(0, dp(12), 0, 0);
        root.addView(toggleRow, toggleParams);
    }

    private LinearLayout powerButton(String text, boolean on, String label,
            int[][] commands) {
        LinearLayout button = new LinearLayout(this);
        button.setOrientation(LinearLayout.HORIZONTAL);
        button.setGravity(Gravity.CENTER);
        button.setPadding(dp(12), 0, dp(12), 0);
        int background = on ? Color.rgb(255, 237, 225) : Color.rgb(240, 238, 247);
        int foreground = on ? Color.rgb(179, 73, 28) : Color.rgb(57, 50, 89);
        button.setBackground(touchBackground(background, blendWithWhite(background), 20));
        button.setClickable(true);
        button.setFocusable(true);
        button.setContentDescription(text);

        RemoteIconView icon = new RemoteIconView(this,
                on ? RemoteIconView.POWER_ON : RemoteIconView.POWER_OFF);
        button.addView(icon, new LinearLayout.LayoutParams(dp(28), dp(28)));

        TextView textView = centeredLabel(text, foreground, 16);
        textView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        LinearLayout.LayoutParams textParams = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT,
                LinearLayout.LayoutParams.WRAP_CONTENT);
        textParams.setMargins(dp(9), 0, 0, 0);
        button.addView(textView, textParams);
        setRemoteAction(button, label, commands);
        return button;
    }

    private void addPairButtons(LinearLayout row, View left, View right) {
        LinearLayout.LayoutParams leftParams = new LinearLayout.LayoutParams(
                0, dp(56), 1);
        leftParams.setMargins(0, 0, dp(6), 0);
        row.addView(left, leftParams);
        LinearLayout.LayoutParams rightParams = new LinearLayout.LayoutParams(
                0, dp(56), 1);
        rightParams.setMargins(dp(6), 0, 0, 0);
        row.addView(right, rightParams);
    }

    private void addRemotePairButtons(LinearLayout row, View left, View right) {
        LinearLayout.LayoutParams leftParams = new LinearLayout.LayoutParams(
                0, dp(72), 1);
        leftParams.setMargins(0, 0, dp(6), 0);
        row.addView(left, leftParams);
        LinearLayout.LayoutParams rightParams = new LinearLayout.LayoutParams(
                0, dp(72), 1);
        rightParams.setMargins(dp(6), 0, 0, 0);
        row.addView(right, rightParams);
    }

    private LinearLayout sceneButton(String text, int iconType, String label,
            int[][] commands, int background, int textColor) {
        LinearLayout button = new LinearLayout(this);
        button.setOrientation(LinearLayout.HORIZONTAL);
        button.setGravity(Gravity.CENTER);
        button.setBackground(touchBackground(background,
                blendWithWhite(background), 20));
        button.setClickable(true);
        button.setFocusable(true);
        button.setContentDescription(text);

        RemoteIconView icon = new RemoteIconView(this, iconType);
        button.addView(icon, new LinearLayout.LayoutParams(dp(28), dp(28)));
        TextView textView = centeredLabel(text, textColor, 16);
        textView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        LinearLayout.LayoutParams textParams = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT,
                LinearLayout.LayoutParams.WRAP_CONTENT);
        textParams.setMargins(dp(9), 0, 0, 0);
        button.addView(textView, textParams);
        setRemoteAction(button, label, commands);
        return button;
    }

    private LinearLayout copyButton(String text, final String value) {
        LinearLayout button = new LinearLayout(this);
        button.setGravity(Gravity.CENTER);
        button.setBackground(touchBackground(Color.rgb(246, 244, 238),
                Color.WHITE, 16));
        button.setClickable(true);
        button.setFocusable(true);
        TextView textView = centeredLabel(text, Color.rgb(75, 70, 61), 14);
        textView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        button.addView(textView, matchWrap());
        button.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                ClipboardManager clipboard = (ClipboardManager)
                        getSystemService(Context.CLIPBOARD_SERVICE);
                if (clipboard != null) {
                    clipboard.setPrimaryClip(ClipData.newPlainText("灯控 API", value));
                    Toast.makeText(MainActivity.this, "已复制", Toast.LENGTH_SHORT).show();
                }
            }
        });
        return button;
    }

    private LinearLayout apiToggleButton(final boolean enabled) {
        LinearLayout button = new LinearLayout(this);
        button.setGravity(Gravity.CENTER);
        button.setBackground(touchBackground(
                enabled ? Color.rgb(246, 244, 238) : Color.rgb(43, 40, 34),
                enabled ? Color.WHITE : Color.rgb(69, 62, 50), 16));
        button.setClickable(true);
        button.setFocusable(true);
        button.setContentDescription(enabled ? "关闭局域网遥控" : "启用局域网遥控");
        TextView textView = centeredLabel(enabled ? "关闭局域网遥控" : "启用局域网遥控",
                enabled ? Color.rgb(75, 70, 61) : Color.WHITE, 15);
        textView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        button.addView(textView, matchWrap());
        button.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                if (enabled) {
                    LightApiService.setEnabled(MainActivity.this, false);
                    stopService(new Intent(MainActivity.this, LightApiService.class));
                    showSettingsScreen();
                    return;
                }
                LightApiService.setEnabled(MainActivity.this, true);
                showSettingsScreen();
                requestApiStart();
            }
        });
        return button;
    }

    private LinearLayout updateCheckButton() {
        LinearLayout button = new LinearLayout(this);
        button.setGravity(Gravity.CENTER);
        button.setBackground(touchBackground(Color.rgb(43, 40, 34),
                Color.rgb(69, 62, 50), 16));
        button.setClickable(true);
        button.setFocusable(true);
        button.setContentDescription("检查更新");
        TextView textView = centeredLabel("检查更新", Color.WHITE, 15);
        textView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        button.addView(textView, matchWrap());
        button.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                if (!appUpdateManager.checkForUpdates(true)) {
                    Toast.makeText(MainActivity.this, "更新任务正在进行",
                            Toast.LENGTH_SHORT).show();
                }
            }
        });
        return button;
    }

    private void showUpdateAvailable(final AppUpdateManager.Release release) {
        String message = "当前版本：v" + appUpdateManager.currentVersionName()
                + "\n最新版本：v" + release.versionName;
        if (!release.releaseNotes.isEmpty()) {
            message += "\n\n" + release.releaseNotes;
        }
        new AlertDialog.Builder(this)
                .setTitle("发现新版本")
                .setMessage(message)
                .setNegativeButton("稍后", null)
                .setPositiveButton("下载并安装", (dialog, which) -> {
                    if (!appUpdateManager.downloadAndInstall(release)) {
                        Toast.makeText(MainActivity.this, "更新任务正在进行",
                                Toast.LENGTH_SHORT).show();
                    }
                })
                .show();
    }

    private void setRemoteAction(View view, final String label,
            final int[][] commands) {
        view.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                view.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP);
                requestRemoteSequence(label, commands);
            }
        });
    }

    private void setAlternatingRemoteAction(View view,
            final String firstLabel, final int[][] firstCommands,
            final String secondLabel, final int[][] secondCommands) {
        final boolean[] sendFirst = {true};
        view.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View view) {
                view.performHapticFeedback(HapticFeedbackConstants.KEYBOARD_TAP);
                if (sendFirst[0]) {
                    requestRemoteSequence(firstLabel, firstCommands);
                } else {
                    requestRemoteSequence(secondLabel, secondCommands);
                }
                sendFirst[0] = !sendFirst[0];
            }
        });
    }

    private int[][] warmCommands() {
        return new int[][] {
                {0x39, 3, 0, 0, 0}, {0x21, 0x18, 0, 2, 0}};
    }

    private int[][] coolCommands() {
        return new int[][] {
                {0x39, 2, 0, 0, 0}, {0x21, 0x24, 0, 2, 0}};
    }

    private void configureWindowColors() {
        getWindow().setStatusBarColor(Color.rgb(255, 240, 188));
        getWindow().setNavigationBarColor(Color.rgb(255, 251, 236));
        int visibility = View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            visibility |= View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR;
        }
        getWindow().getDecorView().setSystemUiVisibility(visibility);
    }

    private LinearLayout card() {
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.VERTICAL);
        card.setPadding(dp(19), dp(18), dp(19), dp(19));
        card.setBackground(rounded(Color.rgb(255, 255, 255), 20));
        card.setElevation(dp(1));
        return card;
    }

    private void addCard(LinearLayout root, LinearLayout card) {
        LinearLayout.LayoutParams params = matchWrap();
        params.setMargins(0, dp(13), 0, 0);
        root.addView(card, params);
    }

    private LinearLayout cardHeader(String title, String detail) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);

        TextView titleView = new TextView(this);
        titleView.setText(title);
        titleView.setTextColor(Color.rgb(29, 27, 25));
        titleView.setTextSize(19);
        titleView.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        row.addView(titleView, new LinearLayout.LayoutParams(0,
                LinearLayout.LayoutParams.WRAP_CONTENT, 1));

        TextView detailView = new TextView(this);
        detailView.setText(detail);
        detailView.setTextColor(Color.rgb(133, 128, 119));
        detailView.setTextSize(13);
        row.addView(detailView, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT,
                LinearLayout.LayoutParams.WRAP_CONTENT));
        return row;
    }

    private TextView centeredLabel(String text, int color, float size) {
        TextView label = new TextView(this);
        label.setText(text);
        label.setTextColor(color);
        label.setTextSize(size);
        label.setGravity(Gravity.CENTER);
        return label;
    }

    private int blendWithWhite(int color) {
        return Color.rgb(
                (Color.red(color) + 255) / 2,
                (Color.green(color) + 255) / 2,
                (Color.blue(color) + 255) / 2);
    }

    private GradientDrawable rounded(int color, float radiusDp) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(color);
        drawable.setCornerRadius(dp(radiusDp));
        return drawable;
    }

    private StateListDrawable touchBackground(int normal, int pressed, float radiusDp) {
        StateListDrawable states = new StateListDrawable();
        states.addState(new int[] {android.R.attr.state_pressed}, rounded(pressed, radiusDp));
        states.addState(new int[] {}, rounded(normal, radiusDp));
        return states;
    }

    private LinearLayout.LayoutParams matchWrap() {
        return new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT);
    }

    private int dp(float value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private void handleIntent(Intent intent) {
        if (intent == null) {
            return;
        }

        scanMinRssi = intent.getIntExtra(BleScanService.EXTRA_MIN_RSSI, DEFAULT_MIN_RSSI);
        String command = intent.getStringExtra("command");

        if ("stop".equals(command)) {
            stopScanning();
        } else if ("mark".equals(command)) {
            writeMarker(intent.getStringExtra("label"));
        } else if ("set-counter".equals(command)) {
            setPhoneCounter(intent.getIntExtra("counter", -1));
        } else if ("rotate-api-token".equals(command)) {
            LightApiService.rotateToken(this);
            statusView.setText("局域网 API Token 已更新");
        } else if ("advertise".equals(command)) {
            requestAdvertise(intent);
        } else if ("start".equals(command)) {
            requestStart();
        } else if ("learn".equals(command)) {
            requestLearningStart();
        }
    }

    private void setPhoneCounter(int counter) {
        if (counter < 0 || counter > 255) {
            statusView.setText("同步失败：序号必须在 0～255 之间");
            return;
        }
        getSharedPreferences(FanLampRemoteProtocol.PREFS_NAME, MODE_PRIVATE)
                .edit().putInt(FanLampRemoteProtocol.PREF_TX_COUNT, counter).apply();
        statusView.setText("发送序号已同步，可以使用");
    }

    private void writeMarker(String label) {
        try {
            JSONObject marker = new JSONObject();
            marker.put("event", "marker");
            marker.put("epoch_ms", System.currentTimeMillis());
            marker.put("label", label == null ? "UNLABELED" : label);
            Log.i("BLE_PROBE", marker.toString());
            statusView.setText("Marker written: " + marker.getString("label"));
        } catch (JSONException impossible) {
            Log.e("BLE_PROBE", "Unable to create marker", impossible);
        }
    }

    private void requestStart() {
        pendingStart = true;
        List<String> missing = missingPermissions();
        if (!missing.isEmpty()) {
            statusView.setText("Waiting for Bluetooth permission");
            requestPermissions(missing.toArray(new String[0]), PERMISSION_REQUEST);
            return;
        }
        startScanning();
    }

    private void requestAdvertise(Intent commandIntent) {
        pendingAdvertiseIntent = new Intent(commandIntent);
        List<String> missing = missingAdvertisePermissions();
        if (!missing.isEmpty()) {
            statusView.setText("Waiting for Bluetooth advertise permission");
            requestPermissions(missing.toArray(new String[0]), PERMISSION_REQUEST);
            return;
        }
        startAdvertising(pendingAdvertiseIntent);
    }

    private void requestRemoteSequence(String label, int[][] commands) {
        List<String> missing = missingAdvertisePermissions();
        if (!missing.isEmpty()) {
            pendingRemoteLabel = label;
            pendingRemoteCommands = commands;
            statusView.setText("等待蓝牙广播权限");
            requestPermissions(missing.toArray(new String[0]), PERMISSION_REQUEST);
            return;
        }
        startRemoteSequence(label, commands);
    }

    private void requestNamedRemoteAction(String action) {
        List<String> missing = missingAdvertisePermissions();
        if (!missing.isEmpty()) {
            pendingNamedRemoteAction = action;
            statusView.setText("等待蓝牙广播权限");
            requestPermissions(missing.toArray(new String[0]), PERMISSION_REQUEST);
            return;
        }
        String label = LightCommandDispatcher.dispatchNamed(this, action);
        statusView.setText("已触发“" + label + "”");
    }

    private void startRemoteSequence(final String label, int[][] commands) {
        LightCommandDispatcher.sendSequence(this, label, commands);
        if (!SCREEN_CONTROL.equals(currentScreen)) {
            statusView.setText("已触发“" + label + "”");
        }
    }

    private void startLanApi() {
        Intent serviceIntent = new Intent(this, LightApiService.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(serviceIntent);
        } else {
            startService(serviceIntent);
        }
    }

    private void requestApiStart() {
        if (!LightApiService.isEnabled(this)) {
            return;
        }
        List<String> missing = missingAdvertisePermissions();
        if (!missing.isEmpty()) {
            pendingApiStart = true;
            statusView.setText("请授权附近设备和通知，以启动局域网 API");
            requestPermissions(missing.toArray(new String[0]), PERMISSION_REQUEST);
            return;
        }
        pendingApiStart = false;
        startLanApi();
        if (statusView != null && SCREEN_HOME.equals(currentScreen)) {
            statusView.setText("●  局域网遥控服务已启动");
        }
    }

    private void startAdvertising(Intent commandIntent) {
        Intent serviceIntent = new Intent(this, BleAdvertiseService.class);
        serviceIntent.putExtra(BleAdvertiseService.EXTRA_SERVICE_DATA,
                commandIntent.getStringExtra(BleAdvertiseService.EXTRA_SERVICE_DATA));
        serviceIntent.putExtra(BleAdvertiseService.EXTRA_MANUFACTURER_DATA,
                commandIntent.getStringExtra(BleAdvertiseService.EXTRA_MANUFACTURER_DATA));
        serviceIntent.putExtra(BleAdvertiseService.EXTRA_DURATION_MS,
                commandIntent.getIntExtra(BleAdvertiseService.EXTRA_DURATION_MS, 2000));
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(serviceIntent);
        } else {
            startService(serviceIntent);
        }
        pendingAdvertiseIntent = null;
        statusView.setText("Advertising command; watch the physical lamp");
    }

    private List<String> missingPermissions() {
        List<String> missing = new ArrayList<>();
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            addIfMissing(missing, Manifest.permission.BLUETOOTH_SCAN);
            addIfMissing(missing, Manifest.permission.BLUETOOTH_CONNECT);
        } else if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            addIfMissing(missing, Manifest.permission.ACCESS_FINE_LOCATION);
        }
        return missing;
    }

    private List<String> missingAdvertisePermissions() {
        List<String> missing = new ArrayList<>();
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            addIfMissing(missing, Manifest.permission.BLUETOOTH_ADVERTISE);
            addIfMissing(missing, Manifest.permission.BLUETOOTH_CONNECT);
        }
        return missing;
    }

    private void addIfMissing(List<String> missing, String permission) {
        if (checkSelfPermission(permission) != PackageManager.PERMISSION_GRANTED) {
            missing.add(permission);
        }
    }

    private void startScanning() {
        pendingStart = false;
        int minRssi = scanMinRssi;
        Intent serviceIntent = new Intent(this, BleScanService.class);
        serviceIntent.setAction(BleScanService.ACTION_START);
        serviceIntent.putExtra(BleScanService.EXTRA_MIN_RSSI, minRssi);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(serviceIntent);
        } else {
            startService(serviceIntent);
        }
        statusView.setText("Scanning; min RSSI " + minRssi
                + " dBm\nRead logs with: adb logcat -v raw -s BLE_PROBE:I '*:S'");
    }

    private void stopScanning() {
        pendingStart = false;
        stopService(new Intent(this, BleScanService.class));
        statusView.setText("Scanner stopped");
    }

    @Override
    public void onRequestPermissionsResult(int requestCode, String[] permissions,
            int[] grantResults) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults);
        if (requestCode == PERMISSION_REQUEST && pendingLearningStart) {
            pendingLearningStart = false;
            if (missingPermissions().isEmpty()) {
                showLearningScreen();
            } else {
                statusView.setText("需要附近设备权限才能识别遥控器");
            }
            if (pendingApiStart && missingAdvertisePermissions().isEmpty()) {
                pendingApiStart = false;
                startLanApi();
            }
            return;
        }
        if (requestCode == PERMISSION_REQUEST && pendingApiStart) {
            pendingApiStart = false;
            if (missingAdvertisePermissions().isEmpty()) {
                startLanApi();
                statusView.setText("●  可以使用");
            } else {
                LightApiService.setEnabled(this, false);
                statusView.setText("需要附近设备权限才能发送灯控指令");
            }
            return;
        }
        if (requestCode == PERMISSION_REQUEST && pendingAdvertiseIntent != null) {
            if (missingAdvertisePermissions().isEmpty()) {
                startAdvertising(pendingAdvertiseIntent);
            } else {
                pendingAdvertiseIntent = null;
                statusView.setText("Bluetooth advertise permission denied");
            }
            return;
        }
        if (requestCode == PERMISSION_REQUEST && pendingNamedRemoteAction != null) {
            String action = pendingNamedRemoteAction;
            pendingNamedRemoteAction = null;
            if (missingAdvertisePermissions().isEmpty()) {
                requestNamedRemoteAction(action);
            } else {
                statusView.setText("蓝牙广播权限被拒绝");
            }
            return;
        }
        if (requestCode == PERMISSION_REQUEST && pendingRemoteCommands != null) {
            if (missingAdvertisePermissions().isEmpty()) {
                int[][] commands = pendingRemoteCommands;
                String label = pendingRemoteLabel;
                pendingRemoteCommands = null;
                pendingRemoteLabel = null;
                startRemoteSequence(label, commands);
            } else {
                pendingRemoteCommands = null;
                pendingRemoteLabel = null;
                statusView.setText("蓝牙广播权限被拒绝");
            }
            return;
        }
        if (requestCode != PERMISSION_REQUEST || !pendingStart) {
            return;
        }
        if (missingPermissions().isEmpty()) {
            startScanning();
        } else {
            pendingStart = false;
            statusView.setText("Required permission denied; grant Nearby devices and notifications");
        }
    }

    @Override
    public void onBackPressed() {
        if (SCREEN_SETTINGS.equals(currentScreen)) {
            showControlScreen();
            return;
        }
        if (SCREEN_CONTROL.equals(currentScreen) || SCREEN_LEARNING.equals(currentScreen)) {
            showHomeScreen();
            return;
        }
        super.onBackPressed();
    }

    @Override
    protected void onDestroy() {
        stopLearning();
        if (appUpdateManager != null) {
            appUpdateManager.close();
        }
        super.onDestroy();
    }

    private static final class RemoteIconView extends View {
        static final int POWER_ON = 1;
        static final int POWER_OFF = 2;
        static final int SUN = 3;
        static final int HALF = 4;
        static final int MINUS = 5;
        static final int PLUS = 6;
        static final int WARM = 7;
        static final int COOL = 8;

        private final int type;
        private final Paint paint = new Paint(Paint.ANTI_ALIAS_FLAG);
        private final float density;

        RemoteIconView(Activity context, int type) {
            super(context);
            this.type = type;
            density = context.getResources().getDisplayMetrics().density;
        }

        @Override
        protected void onDraw(Canvas canvas) {
            super.onDraw(canvas);
            float cx = getWidth() / 2f;
            float cy = getHeight() / 2f;
            float size = Math.min(getWidth(), getHeight());
            if (type == POWER_ON || type == POWER_OFF) {
                drawPower(canvas, cx, cy, size,
                        type == POWER_ON ? Color.rgb(247, 118, 65)
                                : Color.rgb(37, 32, 66));
            } else if (type == SUN || type == WARM) {
                drawSun(canvas, cx, cy, size);
            } else if (type == HALF) {
                drawHalf(canvas, cx, cy, size);
            } else if (type == MINUS || type == PLUS) {
                drawAdjust(canvas, cx, cy, size, type == PLUS);
            } else if (type == COOL) {
                drawSnowflake(canvas, cx, cy, size);
            }
        }

        private void drawPower(Canvas canvas, float cx, float cy, float size, int background) {
            float radius = size * 0.47f;
            paint.setStyle(Paint.Style.FILL);
            paint.setColor(background);
            canvas.drawCircle(cx, cy, radius, paint);

            paint.setStyle(Paint.Style.STROKE);
            paint.setStrokeWidth(Math.max(2f * density, size * 0.065f));
            paint.setStrokeCap(Paint.Cap.ROUND);
            paint.setColor(Color.WHITE);
            float inner = radius * 0.50f;
            RectF arc = new RectF(cx - inner, cy - inner, cx + inner, cy + inner);
            canvas.drawArc(arc, -48f, 276f, false, paint);
            canvas.drawLine(cx, cy - radius * 0.64f, cx, cy - radius * 0.05f, paint);
        }

        private void drawSun(Canvas canvas, float cx, float cy, float size) {
            int orange = Color.rgb(247, 165, 31);
            paint.setColor(orange);
            paint.setStyle(Paint.Style.FILL);
            canvas.drawCircle(cx, cy, size * 0.21f, paint);
            paint.setStyle(Paint.Style.STROKE);
            paint.setStrokeWidth(Math.max(1.6f * density, size * 0.075f));
            paint.setStrokeCap(Paint.Cap.ROUND);
            for (int i = 0; i < 8; i++) {
                double angle = i * Math.PI / 4.0;
                float x1 = cx + (float) Math.cos(angle) * size * 0.31f;
                float y1 = cy + (float) Math.sin(angle) * size * 0.31f;
                float x2 = cx + (float) Math.cos(angle) * size * 0.43f;
                float y2 = cy + (float) Math.sin(angle) * size * 0.43f;
                canvas.drawLine(x1, y1, x2, y2, paint);
            }
        }

        private void drawHalf(Canvas canvas, float cx, float cy, float size) {
            float radius = size * 0.35f;
            int purple = Color.rgb(58, 53, 85);
            paint.setStyle(Paint.Style.FILL);
            paint.setColor(purple);
            canvas.save();
            canvas.clipRect(cx - radius, cy - radius, cx, cy + radius);
            canvas.drawCircle(cx, cy, radius, paint);
            canvas.restore();
            paint.setStyle(Paint.Style.STROKE);
            paint.setStrokeWidth(Math.max(1.3f * density, size * 0.055f));
            canvas.drawCircle(cx, cy, radius, paint);
        }

        private void drawAdjust(Canvas canvas, float cx, float cy, float size,
                boolean plus) {
            paint.setStyle(Paint.Style.FILL);
            paint.setColor(plus ? Color.rgb(238, 168, 38) : Color.rgb(142, 132, 111));
            canvas.drawCircle(cx, cy, size * 0.39f, paint);
            paint.setStyle(Paint.Style.STROKE);
            paint.setStrokeWidth(Math.max(1.8f * density, size * 0.085f));
            paint.setStrokeCap(Paint.Cap.ROUND);
            paint.setColor(Color.WHITE);
            float arm = size * 0.17f;
            canvas.drawLine(cx - arm, cy, cx + arm, cy, paint);
            if (plus) {
                canvas.drawLine(cx, cy - arm, cx, cy + arm, paint);
            }
        }

        private void drawSnowflake(Canvas canvas, float cx, float cy, float size) {
            paint.setStyle(Paint.Style.STROKE);
            paint.setStrokeWidth(Math.max(1.4f * density, size * 0.065f));
            paint.setStrokeCap(Paint.Cap.ROUND);
            paint.setColor(Color.rgb(86, 119, 169));
            float radius = size * 0.38f;
            for (int i = 0; i < 3; i++) {
                double angle = i * Math.PI / 3.0;
                float dx = (float) Math.cos(angle) * radius;
                float dy = (float) Math.sin(angle) * radius;
                canvas.drawLine(cx - dx, cy - dy, cx + dx, cy + dy, paint);
            }
        }
    }
}
