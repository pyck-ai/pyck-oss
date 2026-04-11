FROM node:22-alpine

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies
RUN npm install

# Copy application files
COPY . .

# Expose port
EXPOSE 3000

# Disable TLS verification for development
ENV NODE_TLS_REJECT_UNAUTHORIZED=0

# Start the server
CMD ["npm", "start"]