require('dotenv').config({ path: '../../.env' });
const express = require('express');
const cors = require('cors');
const path = require('path');
const https = require('https');
const axios = require('axios');

const app = express();
const PORT = process.env.PORT || 3000;

// OAuth2 Configuration - Read from environment variables (.env file)
// Check for required environment variables
const requiredEnvVars = ['OIDC_CLIENT_ID', 'OIDC_CLIENT_SECRET', 'PYCK_ZITADEL_AUDIENCE'];
const missingVars = requiredEnvVars.filter(varName => !process.env[varName]);

if (missingVars.length > 0) {
    console.error('❌ Missing required environment variables:');
    missingVars.forEach(varName => console.error(`   - ${varName}`));
    console.error('\nPlease ensure these variables are set in your .env file');
    process.exit(1);
}

const CLIENT_ID = process.env.OIDC_CLIENT_ID;
const CLIENT_SECRET = process.env.OIDC_CLIENT_SECRET;
const PYCK_ZITADEL_AUDIENCE = process.env.PYCK_ZITADEL_AUDIENCE;
const TOKEN_ENDPOINT = `${PYCK_ZITADEL_AUDIENCE}/oauth/v2/token`;

// Parse JSON bodies
app.use(express.json());
// Parse URL-encoded bodies
app.use(express.urlencoded({ extended: true }));

// Enable CORS for all origins in development
app.use(cors());

// Serve static files
app.use(express.static(__dirname));

// Handle OAuth callback by serving the same index.html
app.get('/callback', (req, res) => {
    res.sendFile(path.join(__dirname, 'index.html'));
});

// Token exchange endpoint - handles the client secret on backend
app.post('/api/token-exchange', async (req, res) => {
    const { code, redirect_uri, code_verifier } = req.body;
    
    if (!code) {
        return res.status(400).json({ error: 'Missing authorization code' });
    }
    
    const params = new URLSearchParams();
    params.append('grant_type', 'authorization_code');
    params.append('code', code);
    params.append('redirect_uri', redirect_uri);
    params.append('client_id', CLIENT_ID);
    params.append('client_secret', CLIENT_SECRET);
    
    if (code_verifier) {
        params.append('code_verifier', code_verifier);
    }
    
    try {
        // Make token request with self-signed cert handling
        const httpsAgent = new https.Agent({
            rejectUnauthorized: false
        });
        
        const response = await axios.post(TOKEN_ENDPOINT, params.toString(), {
            headers: {
                'Content-Type': 'application/x-www-form-urlencoded',
            },
            httpsAgent: httpsAgent,
            validateStatus: () => true // Accept any status
        });
        
        if (response.status !== 200) {
            return res.status(response.status).json(response.data);
        }
        
        res.json(response.data);
    } catch (error) {
        console.error('Token exchange error:', error);
        res.status(500).json({ error: 'Token exchange failed', details: error.message });
    }
});

// Configuration endpoint
app.get('/api/config', (req, res) => {
    const config = {
        authority: PYCK_ZITADEL_AUDIENCE,
        client_id: CLIENT_ID,
        redirect_uri: 'http://localhost:4182/callback',
        response_type: 'code',
        scope: 'openid profile email urn:zitadel:iam:user:resourceowner offline_access',
        post_logout_redirect_uri: 'http://localhost:4182/',
        automaticSilentRenew: false,
        loadUserInfo: true,
        metadata: {
            issuer: PYCK_ZITADEL_AUDIENCE,
            authorization_endpoint: `${PYCK_ZITADEL_AUDIENCE}/oauth/v2/authorize`,
            token_endpoint: 'http://localhost:4182/api/token-exchange',
            userinfo_endpoint: `${PYCK_ZITADEL_AUDIENCE}/oidc/v1/userinfo`,
            jwks_uri: `${PYCK_ZITADEL_AUDIENCE}/oidc/v1/keys`,
            end_session_endpoint: `${PYCK_ZITADEL_AUDIENCE}/oidc/v1/end_session`
        }
    };
    res.json(config);
});

// Health check
app.get('/health', (req, res) => {
    res.json({ 
        status: 'OK',
        service: 'OIDC Debug Frontend',
        timestamp: new Date().toISOString()
    });
});

// Start server
app.listen(PORT, () => {
    console.log(`OIDC Debug Frontend running on port ${PORT}`);
    console.log(`Access at: http://localhost:4182`);
});