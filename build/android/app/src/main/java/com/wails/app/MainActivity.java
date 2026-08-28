package com.wails.app;

import android.annotation.SuppressLint;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.util.Log;
import android.view.ViewGroup;
import android.webkit.RenderProcessGoneDetail;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;

import androidx.activity.OnBackPressedCallback;
import androidx.annotation.Nullable;
import androidx.appcompat.app.AppCompatActivity;
import androidx.core.graphics.Insets;
import androidx.core.view.ViewCompat;
import androidx.core.view.WindowCompat;
import androidx.core.view.WindowInsetsCompat;
import androidx.webkit.WebSettingsCompat;
import androidx.webkit.WebViewAssetLoader;
import androidx.webkit.WebViewFeature;
import com.wails.app.BuildConfig;

/**
 * MainActivity hosts the WebView and manages the Wails application lifecycle.
 * It uses WebViewAssetLoader to serve assets from the Go library without
 * requiring a network server.
 */
public class MainActivity extends AppCompatActivity {
    private static final String TAG = "WailsActivity";
    private static final String WAILS_SCHEME = "https";
    private static final String WAILS_HOST = "wails.localhost";

    private WebView webView;
    private WailsBridge bridge;
    private WebViewAssetLoader assetLoader;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        WindowCompat.enableEdgeToEdge(getWindow());
        setContentView(R.layout.activity_main);
        configureSystemBarInsets();
        configureBackNavigation();

        // Initialize the native Go library
        bridge = new WailsBridge(this);
        bridge.initialize();

        // Set up WebView
        setupWebView();

        // Load the application
        loadApplication();
    }

    @SuppressLint("SetJavaScriptEnabled")
    private void setupWebView() {
        webView = findViewById(R.id.webview);

        // Configure WebView settings
        WebSettings settings = webView.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setAllowFileAccess(false);
        settings.setAllowContentAccess(false);
        settings.setMediaPlaybackRequiresUserGesture(false);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        if (WebViewFeature.isFeatureSupported(WebViewFeature.DOWNLOAD_FAVICONS_ENABLED)) {
            WebSettingsCompat.setDownloadFaviconsEnabled(settings, false);
        }

        // Enable debugging in debug builds
        if (BuildConfig.DEBUG) {
            WebView.setWebContentsDebuggingEnabled(true);
        }

        // Set up asset loader for serving local assets
        assetLoader = new WebViewAssetLoader.Builder()
                .setDomain(WAILS_HOST)
                .addPathHandler("/", new WailsPathHandler(bridge))
                .build();

        // Set up WebView client to intercept requests
        webView.setWebViewClient(new WebViewClient() {
            @Nullable
            @Override
            public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
                String url = request.getUrl().toString();
                Log.d(TAG, "Intercepting request: " + url);

                // Only the app's exact HTTPS origin may reach the embedded asset/runtime bridge.
                if (isWailsOrigin(request.getUrl())) {

                    // For wails API calls (runtime, capabilities, etc.), we need to pass the full URL
                    // including query string because WebViewAssetLoader.PathHandler strips query params
                    String path = request.getUrl().getPath();
                    if (path != null && path.startsWith("/wails/")) {
                        // Get full path with query string for runtime calls
                        String fullPath = path;
                        String query = request.getUrl().getQuery();
                        if (query != null && !query.isEmpty()) {
                            fullPath = path + "?" + query;
                        }
                        Log.d(TAG, "Wails API call detected, full path: " + fullPath);

                        // Call bridge directly with full path
                        byte[] data = bridge.serveAsset(fullPath, request.getMethod(), "{}");
                        if (data != null && data.length > 0) {
                            java.io.InputStream inputStream = new java.io.ByteArrayInputStream(data);
                            java.util.Map<String, String> headers = new java.util.HashMap<>();
                            headers.put("Access-Control-Allow-Origin", "*");
                            headers.put("Cache-Control", "no-cache");
                            headers.put("Content-Type", "application/json");

                            return new WebResourceResponse(
                                "application/json",
                                "UTF-8",
                                200,
                                "OK",
                                headers,
                                inputStream
                            );
                        }
                        // Return error response if data is null
                        return new WebResourceResponse(
                            "application/json",
                            "UTF-8",
                            500,
                            "Internal Error",
                            new java.util.HashMap<>(),
                            new java.io.ByteArrayInputStream("{}".getBytes())
                        );
                    }

                    // For regular assets, use the asset loader
                    return assetLoader.shouldInterceptRequest(request.getUrl());
                }

                // addJavascriptInterface is visible to every frame, while
                // shouldOverrideUrlLoading is not called for every request (notably POST).
                // Fail closed for all content outside the app origin. Normal external GET
                // navigation is handed to the system browser by shouldOverrideUrlLoading.
                Log.w(TAG, "Blocking non-Wails WebView request: " + url);
                return new WebResourceResponse(
                    "text/plain",
                    "UTF-8",
                    403,
                    "Forbidden",
                    java.util.Collections.emptyMap(),
                    new java.io.ByteArrayInputStream(new byte[0])
                );
            }

            @Override
            public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                Uri uri = request.getUrl();
                if (isWailsOrigin(uri)) {
                    return false;
                }
                if (!request.isForMainFrame()) {
                    return true;
                }
                String scheme = uri.getScheme();
                if ("https".equalsIgnoreCase(scheme) || "http".equalsIgnoreCase(scheme)) {
                    try {
                        startActivity(new Intent(Intent.ACTION_VIEW, uri));
                    } catch (ActivityNotFoundException error) {
                        Log.w(TAG, "No browser available for external URL", error);
                    }
                }
                return true;
            }

            @Override
            public void onPageFinished(WebView view, String url) {
                super.onPageFinished(view, url);
                if (!isWailsOrigin(Uri.parse(url))) {
                    Log.w(TAG, "Refusing runtime injection outside the Wails origin");
                    return;
                }
                Log.d(TAG, "Page loaded: " + url);
                // Inject Wails runtime
                bridge.injectRuntime(webView, url);
            }

            @Override
            public boolean onRenderProcessGone(WebView view, RenderProcessGoneDetail detail) {
                Log.e(TAG, "WebView renderer exited; crashed=" + detail.didCrash());
                if (view.getParent() instanceof ViewGroup parent) {
                    parent.removeView(view);
                }
                view.destroy();
                if (view == webView) {
                    webView = null;
                }
                finishAndRemoveTask();
                return true;
            }
        });

        // Add JavaScript interface for Go communication
        webView.addJavascriptInterface(new WailsJSBridge(bridge, webView), "wails");
    }

    private void loadApplication() {
        // Load the main page from the asset server
        String url = WAILS_SCHEME + "://" + WAILS_HOST + "/";
        Log.d(TAG, "Loading URL: " + url);
        webView.loadUrl(url);
    }

    private static boolean isWailsOrigin(Uri uri) {
        if (uri == null
                || !WAILS_SCHEME.equalsIgnoreCase(uri.getScheme())
                || !WAILS_HOST.equalsIgnoreCase(uri.getHost())) {
            return false;
        }
        int port = uri.getPort();
        return port == -1 || port == 443;
    }

    private void configureSystemBarInsets() {
        ViewCompat.setOnApplyWindowInsetsListener(findViewById(R.id.main_container), (view, windowInsets) -> {
            Insets insets = windowInsets.getInsets(
                    WindowInsetsCompat.Type.systemBars() | WindowInsetsCompat.Type.displayCutout());
            view.setPadding(insets.left, insets.top, insets.right, insets.bottom);
            return windowInsets;
        });
    }

    private void configureBackNavigation() {
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                if (webView != null && webView.canGoBack()) {
                    webView.goBack();
                    return;
                }
                setEnabled(false);
                getOnBackPressedDispatcher().onBackPressed();
            }
        });
    }

    /**
     * Execute JavaScript in the WebView from the Go side
     */
    public void executeJavaScript(final String js) {
        runOnUiThread(() -> {
            if (webView != null) {
                webView.evaluateJavascript(js, null);
            }
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (bridge != null) {
            bridge.onResume();
        }
    }

    @Override
    protected void onPause() {
        super.onPause();
        if (bridge != null) {
            bridge.onPause();
        }
    }

    @Override
    protected void onDestroy() {
        if (bridge != null) {
            bridge.shutdown();
            bridge = null;
        }
        if (webView != null) {
            webView.removeJavascriptInterface("wails");
            webView.destroy();
            webView = null;
        }
        super.onDestroy();
    }
}
