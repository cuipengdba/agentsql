FROM dataease/sqlbot:v1.10.2@sha256:1a573b42481b4cf95ac225ed9e93fbc6821d56f1ced859f779c0e014df24acb4
COPY integrations/sqlbot /tmp/agentsql-sqlbot
RUN python3 /tmp/agentsql-sqlbot/install.py --sqlbot-app /opt/sqlbot/app
